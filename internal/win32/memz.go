//go:build windows

package win32

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type MemzOptions struct {
	Duration        time.Duration
	WindowCount     int
	MemoryCapMB     int
	RestartDelay    time.Duration
	WatchdogSeconds int
	DryRun          bool
}

type MemzReport struct {
	Windows          int
	PeakMB           int
	Duration         time.Duration
	RestartScheduled bool
	CountdownS       int
	WatchdogPID      int
	Err              string
}

var (
	procUpdateWindow       = user32.NewProc("UpdateWindow")
	procPeekMessageW       = user32.NewProc("PeekMessageW")
	procInvalidateRect     = user32.NewProc("InvalidateRect")
	procBeginPaint         = user32.NewProc("BeginPaint")
	procEndPaint           = user32.NewProc("EndPaint")
	procGetClientRect      = user32.NewProc("GetClientRect")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procSetCursorPos       = user32.NewProc("SetCursorPos")
	procFillRect           = user32.NewProc("FillRect")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procTextOutW           = gdi32.NewProc("TextOutW")
	procGlobalMemoryStatus = kernel32.NewProc("GlobalMemoryStatusEx")
	procBeep               = kernel32.NewProc("Beep")
	procInitiateShutdownW  = advapi32.NewProc("InitiateShutdownW")
	procAbortShutdownW     = advapi32.NewProc("AbortSystemShutdownW")
)

const (
	wmPaint         = 0x000F
	wmNCHitTest     = 0x0084
	pmRemove        = 0x0001
	transparent     = 1
	notSrcCopy      = 0x00330008
	wsExNoActivate  = 0x08000000
	htTransparent   = ^uintptr(0)
	memzOverlayID   = 2
	memzWindowTimer = 1

	shutdownRestart     = 0x00000004
	shutdownForceOthers = 0x00000001
	shutdownForceSelf   = 0x00000002
	shutdownPlanned     = 0x80000000

	memzChunkBytes = 32 << 20
	memzNoWindow   = 0x08000000
)

type memzRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type memzPoint struct {
	X int32
	Y int32
}

type memzPaint struct {
	HDC       uintptr
	Erase     int32
	Paint     memzRect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type memzMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      memzPoint
	Private uint32
}

type memzWindowClass struct {
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

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailExtendedVirtual uint64
}

type memzOverlay struct {
	hwnd     uintptr
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	previous uintptr
	width    int32
	height   int32
	frames   int
	invert   bool
}

var (
	memzClassName = "GoVoidMemzWnd"
	memzCallback  uintptr
	memzOnce      sync.Once
	memzSurface   memzOverlay
	memzRunning   atomic.Bool
	memzArmed     atomic.Bool
	memzCancel    atomic.Bool
)

func RunMemz(opts MemzOptions) MemzReport {
	started := time.Now()

	if !memzRunning.CompareAndSwap(false, true) {
		return MemzReport{Err: "ya hay una simulación en curso"}
	}
	defer memzRunning.Store(false)

	if opts.Duration <= 0 {
		opts.Duration = 15 * time.Second
	}
	if opts.Duration > 40*time.Second {
		opts.Duration = 40 * time.Second
	}
	if opts.WindowCount <= 0 {
		opts.WindowCount = 14
	}
	if opts.WindowCount > 24 {
		opts.WindowCount = 24
	}
	if opts.MemoryCapMB <= 0 {
		opts.MemoryCapMB = 512
	}
	if opts.RestartDelay <= 0 {
		opts.RestartDelay = 12 * time.Second
	}
	if opts.WatchdogSeconds <= 0 {
		opts.WatchdogSeconds = int(opts.Duration/time.Second) + int(opts.RestartDelay/time.Second) + 60
	}

	report := MemzReport{}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Duration)
	defer cancel()

	stop := make(chan struct{})
	created := make(chan int, 1)
	finished := make(chan struct{})

	go runMemzWindows(stop, created, finished, opts.WindowCount)

	select {
	case count := <-created:
		report.Windows = count
	case <-time.After(4 * time.Second):
		report.Windows = 0
	}

	memoryDone := make(chan int, 1)
	go func() { memoryDone <- holdMemory(ctx, opts.MemoryCapMB) }()

	beepDone := make(chan struct{})
	go runMemzBeeps(ctx, beepDone)

	jitterDone := make(chan struct{})
	go runMemzJitter(ctx, jitterDone)

	if !opts.DryRun {
		if pid, err := spawnMemzWatchdog(opts.WatchdogSeconds); err != nil {
			report.Err = "vigilante: " + err.Error()
		} else {
			report.WatchdogPID = pid
		}
	}

	<-ctx.Done()

	close(stop)
	<-finished
	<-beepDone
	<-jitterDone

	report.PeakMB = <-memoryDone

	time.Sleep(300 * time.Millisecond)

	if !opts.DryRun {
		armMemzRestart(opts.RestartDelay)
		report.RestartScheduled = true
		report.CountdownS = int(opts.RestartDelay / time.Second)
	}

	report.Duration = time.Since(started)
	return report
}

func runMemzWindows(stop <-chan struct{}, created chan<- int, finished chan<- struct{}, count int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(finished)

	memzOnce.Do(func() {
		memzCallback = syscall.NewCallback(memzWndProc)
		classNamePtr, classBuf := memzUTF16(memzClassName)
		class := memzWindowClass{
			Size:      uint32(unsafe.Sizeof(memzWindowClass{})),
			WndProc:   memzCallback,
			ClassName: classNamePtr,
		}
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
		runtime.KeepAlive(classBuf)
	})

	width, _, _ := procGetSystemMetrics.Call(smCXScreen)
	height, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if width == 0 || height == 0 {
		created <- 0
		return
	}

	startMemzOverlay(int32(width), int32(height))

	handles := make([]uintptr, 0, count)
	for index := 0; index < count; index++ {
		className, classBuf := memzUTF16(memzClassName)
		title, titleBuf := memzUTF16(randomMemzTitle())
		hwnd, _, _ := procCreateWindowExW.Call(
			wsExTopmost|wsExToolWindow|wsExNoActivate,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(title)),
			wsPopup|wsVisible,
			uintptr(rand.Intn(int(width))),
			uintptr(rand.Intn(int(height))),
			uintptr(180+rand.Intn(260)),
			uintptr(110+rand.Intn(190)),
			0, 0, 0, 0,
		)
		runtime.KeepAlive(classBuf)
		runtime.KeepAlive(titleBuf)
		if hwnd == 0 {
			continue
		}
		handles = append(handles, hwnd)
		procShowWindow.Call(hwnd, swShow)
		procUpdateWindow.Call(hwnd)
		procSetTimer.Call(hwnd, memzWindowTimer, uintptr(45+rand.Intn(45)), 0)
	}

	created <- len(handles)

	var message memzMsg
	for {
		select {
		case <-stop:
			stopMemzOverlay()
			for _, hwnd := range handles {
				procKillTimer.Call(hwnd, memzWindowTimer)
				procDestroyWindow.Call(hwnd)
			}
			return
		default:
		}

		for {
			result, _, _ := procPeekMessageW.Call(
				uintptr(unsafe.Pointer(&message)), 0, 0, 0, pmRemove)
			if result == 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
		}
		time.Sleep(8 * time.Millisecond)
	}
}

func memzWndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	switch message {
	case wmEraseBackground:
		return 1
	case wmNCHitTest:
		if hwnd == memzSurface.hwnd {
			return htTransparent
		}
	case wmTimer:
		if hwnd == memzSurface.hwnd {
			if wparam == memzOverlayID {
				memzSurface.frames++
				if memzSurface.frames%16 == 0 {
					memzSurface.invert = !memzSurface.invert
				}
			}
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmPaint:
		var paint memzPaint
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		if hdc != 0 {
			if hwnd == memzSurface.hwnd {
				paintMemzOverlay(hdc)
			} else {
				paintMemzWindow(hdc, hwnd)
			}
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		}
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)
	return result
}

func startMemzOverlay(width, height int32) {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return
	}
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		procReleaseDC.Call(0, screenDC)
		return
	}
	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return
	}
	previous, _, _ := procSelectObject.Call(memDC, bitmap)
	procBitBlt.Call(memDC, 0, 0, uintptr(width), uintptr(height),
		screenDC, 0, 0, srcCopy)

	className, classBuf := memzUTF16(memzClassName)
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(className)),
		0,
		wsPopup|wsVisible,
		0, 0,
		uintptr(width), uintptr(height),
		0, 0, 0, 0,
	)
	runtime.KeepAlive(classBuf)
	if hwnd == 0 {
		procSelectObject.Call(memDC, previous)
		procDeleteObject.Call(bitmap)
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return
	}

	memzSurface = memzOverlay{
		hwnd:     hwnd,
		screenDC: screenDC,
		memDC:    memDC,
		bitmap:   bitmap,
		previous: previous,
		width:    width,
		height:   height,
	}

	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	procSetTimer.Call(hwnd, memzOverlayID, 40, 0)
}

func stopMemzOverlay() {
	if memzSurface.hwnd == 0 {
		return
	}
	procKillTimer.Call(memzSurface.hwnd, memzOverlayID)
	procDestroyWindow.Call(memzSurface.hwnd)
	procSelectObject.Call(memzSurface.memDC, memzSurface.previous)
	procDeleteObject.Call(memzSurface.bitmap)
	procDeleteDC.Call(memzSurface.memDC)
	procReleaseDC.Call(0, memzSurface.screenDC)
	memzSurface = memzOverlay{}
}

func paintMemzOverlay(hdc uintptr) {
	if memzSurface.memDC == 0 || memzSurface.width <= 0 || memzSurface.height <= 0 {
		return
	}

	operation := uintptr(srcCopy)
	if memzSurface.invert {
		operation = notSrcCopy
	}
	procBitBlt.Call(hdc, 0, 0, uintptr(memzSurface.width), uintptr(memzSurface.height),
		memzSurface.memDC, 0, 0, operation)

	for index := 0; index < 28; index++ {
		block := memzRect{
			Left:   int32(rand.Intn(int(memzSurface.width))),
			Top:    int32(rand.Intn(int(memzSurface.height))),
			Right:  0,
			Bottom: 0,
		}
		block.Right = block.Left + int32(12+rand.Intn(220))
		block.Bottom = block.Top + int32(6+rand.Intn(70))
		brush, _, _ := procCreateSolidBrush.Call(uintptr(randomMemzColor()))
		if brush == 0 {
			continue
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&block)), brush)
		procDeleteObject.Call(brush)
	}

	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, uintptr(randomMemzColor()))

	for index := 0; index < 10; index++ {
		text := windows.StringToUTF16(randomMemzLine())
		x := int32(rand.Intn(int(memzSurface.width) - 40))
		y := int32(rand.Intn(int(memzSurface.height) - 24))
		procTextOutW.Call(hdc, uintptr(x), uintptr(y),
			uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1))
		runtime.KeepAlive(text)
	}
}

func paintMemzWindow(hdc, hwnd uintptr) {
	var client memzRect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))

	brush, _, _ := procCreateSolidBrush.Call(uintptr(randomMemzColor()))
	if brush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&client)), brush)
		procDeleteObject.Call(brush)
	}

	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, uintptr(randomMemzColor()))

	lines := 3 + rand.Intn(3)
	for index := 0; index < lines; index++ {
		text := windows.StringToUTF16(randomMemzLine())
		x := int32(rand.Intn(60))
		y := int32(8 + index*22 + rand.Intn(8))
		procTextOutW.Call(hdc, uintptr(x), uintptr(y),
			uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1))
		runtime.KeepAlive(text)
	}
}

func randomMemzColor() uint32 {
	return uint32(rand.Intn(0xFFFFFF))
}

func randomMemzLine() string {
	lines := []string{
		"MEMZ",
		"GoVoid",
		"OUT OF MEMORY",
		"0x" + strings.ToUpper(strconv.FormatInt(int64(rand.Uint32()), 16)),
		"MEMORY COLLAPSE",
		"SYSTEM FAILURE",
		"kernel panic",
		"stack overflow",
		"access violation",
		"rebooting soon",
	}
	return lines[rand.Intn(len(lines))]
}

func randomMemzTitle() string {
	titles := []string{
		"MEMZ",
		"memz.exe",
		"GoVoid",
		"out of memory",
		"system32",
		"kernel",
	}
	return titles[rand.Intn(len(titles))]
}

func holdMemory(ctx context.Context, capMB int) int {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	procGlobalMemoryStatus.Call(uintptr(unsafe.Pointer(&status)))

	limitMB := capMB
	if available := int(status.AvailPhys / (1 << 20)); available > 0 {
		if third := available / 3; third < limitMB {
			limitMB = third
		}
	}
	if limitMB > 1024 {
		limitMB = 1024
	}
	if limitMB < 32 {
		limitMB = 32
	}

	chunks := make([][]byte, 0, limitMB/32+1)
	allocated := 0
	for allocated < limitMB {
		select {
		case <-ctx.Done():
			releaseMemzChunks(chunks)
			return allocated
		default:
		}
		chunk := make([]byte, memzChunkBytes)
		for offset := 0; offset < len(chunk); offset += 4096 {
			chunk[offset] = byte(offset)
		}
		chunks = append(chunks, chunk)
		allocated += 32
		time.Sleep(150 * time.Millisecond)
	}

	<-ctx.Done()
	releaseMemzChunks(chunks)
	return allocated
}

func releaseMemzChunks(chunks [][]byte) {
	for index := range chunks {
		chunks[index] = nil
	}
	debug.FreeOSMemory()
}

func runMemzBeeps(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		frequency := uint32(180 + rand.Intn(1100))
		duration := uint32(70 + rand.Intn(110))
		procBeep.Call(uintptr(frequency), uintptr(duration))
		time.Sleep(time.Duration(90+rand.Intn(240)) * time.Millisecond)
	}
}

func runMemzJitter(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var point memzPoint
			if result, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); result == 0 {
				continue
			}
			point.X += int32(rand.Intn(5) - 2)
			point.Y += int32(rand.Intn(5) - 2)
			procSetCursorPos.Call(uintptr(point.X), uintptr(point.Y))
		}
	}
}

func armMemzRestart(delay time.Duration) {
	memzCancel.Store(false)
	memzArmed.Store(true)

	go func() {
		deadline := time.Now().Add(delay)
		for time.Now().Before(deadline) {
			if memzCancel.Load() {
				memzArmed.Store(false)
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		if memzCancel.Load() {
			memzArmed.Store(false)
			return
		}
		if err := scheduleMemzRestart(0); err != nil {
			memzArmed.Store(false)
			return
		}
		_ = os.Remove(memzWatchdogPath())
	}()
}

func scheduleMemzRestart(delay time.Duration) error {
	if err := enablePrivilege("SeShutdownPrivilege"); err != nil {
		return err
	}

	seconds := uint32(delay / time.Second)
	result, _, _ := procInitiateShutdownW.Call(
		0, 0,
		uintptr(seconds),
		uintptr(shutdownRestart|shutdownForceOthers|shutdownForceSelf),
		uintptr(shutdownPlanned),
	)
	if result != 0 {
		return fmt.Errorf("InitiateShutdownW devolvió %d", result)
	}
	return nil
}

func AbortMemzRestart() error {
	memzCancel.Store(true)
	wasArmed := memzArmed.Swap(false)

	_ = os.Remove(memzWatchdogPath())

	if err := enablePrivilege("SeShutdownPrivilege"); err != nil {
		return err
	}

	result, _, callErr := procAbortShutdownW.Call(0)
	if result != 0 {
		return nil
	}
	if wasArmed {
		return nil
	}
	if callErr == nil || callErr == windows.ERROR_SUCCESS {
		return errors.New("no hay un reinicio pendiente")
	}
	return callErr
}

func RunMemzWatchdog(seconds int) {
	if seconds <= 0 {
		seconds = 90
	}
	if seconds > 900 {
		seconds = 900
	}

	path := memzWatchdogPath()
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(time.Second)
	}

	if _, err := os.Stat(path); err != nil {
		return
	}
	_ = scheduleMemzRestart(0)
	time.Sleep(10 * time.Second)
}

func memzWatchdogPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "GoVoid", "memz.watchdog")
}

func spawnMemzWatchdog(seconds int) (int, error) {
	path := memzWatchdogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(seconds)), 0o644); err != nil {
		return 0, err
	}

	exe, err := os.Executable()
	if err != nil {
		_ = os.Remove(path)
		return 0, err
	}
	cmd := exec.Command(exe, "--memz-watchdog", strconv.Itoa(seconds))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: memzNoWindow}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(path)
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func terminatePID(pid uint32) {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	_ = windows.TerminateProcess(handle, 1)
}

func memzUTF16(value string) (*uint16, []uint16) {
	buf := windows.StringToUTF16(value)
	return &buf[0], buf
}

func MemzDefaults() MemzOptions {
	return MemzOptions{
		Duration:        15 * time.Second,
		WindowCount:     14,
		MemoryCapMB:     512,
		RestartDelay:    12 * time.Second,
		WatchdogSeconds: 90,
	}
}

func MemzSummary(report MemzReport) string {
	var out strings.Builder
	fmt.Fprintf(&out, "ventanas=%d memoria=%dMB tiempo=%dms reinicio=%v",
		report.Windows, report.PeakMB, report.Duration.Milliseconds(), report.RestartScheduled)
	if report.CountdownS > 0 {
		fmt.Fprintf(&out, " cuenta=%ds", report.CountdownS)
	}
	if report.WatchdogPID > 0 {
		fmt.Fprintf(&out, " vigilante=%d", report.WatchdogPID)
	}
	if report.Err != "" {
		fmt.Fprintf(&out, " error=%s", report.Err)
	}
	return out.String()
}
