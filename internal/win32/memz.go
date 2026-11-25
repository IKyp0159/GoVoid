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
	MemoryCapMB     int
	RestartDelay    time.Duration
	WatchdogSeconds int
	DryRun          bool
}

type MemzReport struct {
	Stages           []string
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
	procEnumWindows        = user32.NewProc("EnumWindows")
	procGetClassNameW      = user32.NewProc("GetClassNameW")
	procSendMessageW       = user32.NewProc("SendMessageW")
	procFindWindowExW      = user32.NewProc("FindWindowExW")
	procEllipse            = gdi32.NewProc("Ellipse")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procTextOutW           = gdi32.NewProc("TextOutW")
	procGlobalMemoryStatus = kernel32.NewProc("GlobalMemoryStatusEx")
	procBeep               = kernel32.NewProc("Beep")
	procInitiateShutdownW  = advapi32.NewProc("InitiateShutdownW")
	procAbortShutdownW     = advapi32.NewProc("AbortSystemShutdownW")
)

const (
	wmPaint        = 0x000F
	wmNCHitTest    = 0x0084
	wmChar         = 0x0102
	pmRemove       = 0x0001
	transparent    = 1
	notSrcCopy     = 0x00330008
	wsExNoActivate = 0x08000000
	htTransparent  = ^uintptr(0)
	swHide         = 0

	memzOverlayID = 1
	memzFrameMS   = 40

	shutdownRestart     = 0x00000004
	shutdownForceOthers = 0x00000001
	shutdownForceSelf   = 0x00000002
	shutdownPlanned     = 0x80000000

	memzChunkBytes = 32 << 20
	memzNoWindow   = 0x08000000

	memzModeIdle   = 0
	memzModeTiles  = 1
	memzModeRain   = 2
	memzModeNyan   = 3
	memzModeInvert = 4

	stageJitterEnd = 5 * time.Second
	stageURLsEnd   = 10 * time.Second
	stageInvertEnd = 20 * time.Second
	stageBeepsEnd  = 30 * time.Second
	stageTilesEnd  = 40 * time.Second
	stageRainEnd   = 50 * time.Second
	stageNyanStart = 60 * time.Second
	stageNyanEnd   = 70 * time.Second
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
	mode     int32
	frames   int
	rain     int32
	nyanX    int32
	nyanY    int32
	visible  bool
	ready    bool
}

var (
	memzClassName  = "GoVoidMemzWnd"
	memzCallback   uintptr
	memzOnce       sync.Once
	memzSurface    memzOverlay
	memzRunning    atomic.Bool
	memzArmed      atomic.Bool
	memzCancel     atomic.Bool
	memzNotepad    uintptr
	memzEnumProc   uintptr
	memzStageNames []string
)

func RunMemz(opts MemzOptions) MemzReport {
	started := time.Now()

	if !memzRunning.CompareAndSwap(false, true) {
		return MemzReport{Err: "ya hay una simulación en curso"}
	}
	defer memzRunning.Store(false)

	if opts.Duration <= 0 {
		opts.Duration = stageNyanEnd
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
	memzStageNames = nil

	ctx, cancel := context.WithTimeout(context.Background(), opts.Duration)
	defer cancel()

	stop := make(chan struct{})
	ready := make(chan struct{})
	finished := make(chan struct{})

	go runMemzUI(stop, ready, finished)
	select {
	case <-ready:
	case <-time.After(4 * time.Second):
		report.Err = "overlay no disponible"
	}

	memoryDone := make(chan int, 1)
	go func() { memoryDone <- holdMemory(ctx, opts.MemoryCapMB) }()

	if !opts.DryRun {
		if pid, err := spawnMemzWatchdog(opts.WatchdogSeconds); err != nil {
			report.Err = "vigilante: " + err.Error()
		} else {
			report.WatchdogPID = pid
		}
	}

	stage := func(name string) { memzStageNames = append(memzStageNames, name) }

	stage("cursor")
	waitUntil(started, stageJitterEnd, jitterCursorOnce)

	stage("urls")
	openRandomURLs()

	waitUntil(started, stageURLsEnd, nil)
	stage("invert")
	captureDesktop()
	setMemzMode(memzModeInvert)
	waitUntil(started, stageInvertEnd, nil)
	stage("beep")
	beepUntil(started, stageBeepsEnd)

	waitUntil(started, stageBeepsEnd, nil)
	stage("tiles")
	captureDesktop()
	setMemzMode(memzModeTiles)
	sleepUntil(started, stageTilesEnd)

	stage("rain")
	captureDesktop()
	setMemzMode(memzModeRain)
	sleepUntil(started, stageRainEnd)

	stage("notepad")
	setMemzMode(memzModeIdle)
	notepadType()
	sleepUntil(started, stageNyanStart)

	stage("nyan")
	setMemzMode(memzModeNyan)
	sleepUntil(started, stageNyanEnd)

	setMemzMode(memzModeIdle)

	close(stop)
	<-finished

	report.PeakMB = <-memoryDone

	if !opts.DryRun {
		armMemzRestart(opts.RestartDelay)
		report.RestartScheduled = true
		report.CountdownS = int(opts.RestartDelay / time.Second)
	}

	report.Stages = append([]string(nil), memzStageNames...)
	report.Duration = time.Since(started)
	return report
}

func sleepUntil(start time.Time, at time.Duration) {
	remaining := time.Until(start.Add(at))
	if remaining > 0 {
		time.Sleep(remaining)
	}
}

func waitUntil(start time.Time, at time.Duration, tick func()) {
	for time.Now().Before(start.Add(at)) {
		if tick != nil {
			tick()
		}
		time.Sleep(70 * time.Millisecond)
	}
}

func runMemzUI(stop <-chan struct{}, ready chan<- struct{}, finished chan<- struct{}) {
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
		close(ready)
		return
	}

	createMemzOverlay(int32(width), int32(height))
	close(ready)

	var message memzMsg
	for {
		select {
		case <-stop:
			stopMemzOverlay()
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

func createMemzOverlay(width, height int32) {
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
		ready:    true,
	}

	procShowWindow.Call(hwnd, swHide)
	procSetTimer.Call(hwnd, memzOverlayID, memzFrameMS, 0)
}

func stopMemzOverlay() {
	if !memzSurface.ready {
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

func setMemzMode(mode int32) {
	if !memzSurface.ready {
		return
	}
	memzSurface.mode = mode
	memzSurface.rain = 0
	memzSurface.nyanX = 0
	memzSurface.nyanY = memzSurface.height / 2

	show := uintptr(swShow)
	if mode == memzModeIdle {
		show = swHide
	}
	procShowWindow.Call(memzSurface.hwnd, show)
	procInvalidateRect.Call(memzSurface.hwnd, 0, 1)
}

func captureDesktop() {
	if !memzSurface.ready {
		return
	}
	procBitBlt.Call(memzSurface.memDC, 0, 0,
		uintptr(memzSurface.width), uintptr(memzSurface.height),
		memzSurface.screenDC, 0, 0, srcCopy)
}

func memzWndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	switch message {
	case wmEraseBackground:
		return 1
	case wmNCHitTest:
		return htTransparent
	case wmTimer:
		if wparam == memzOverlayID {
			memzSurface.frames++
			if memzSurface.mode == memzModeRain {
				memzSurface.rain += 6
			}
			if memzSurface.mode == memzModeNyan {
				memzSurface.nyanX += 14
				if memzSurface.nyanX > memzSurface.width+160 {
					memzSurface.nyanX = -160
				}
			}
		}
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmPaint:
		var paint memzPaint
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		if hdc != 0 {
			paintMemzOverlay(hdc)
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		}
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)
	return result
}

func paintMemzOverlay(hdc uintptr) {
	switch memzSurface.mode {
	case memzModeInvert:
		procBitBlt.Call(hdc, 0, 0,
			uintptr(memzSurface.width), uintptr(memzSurface.height),
			memzSurface.memDC, 0, 0, notSrcCopy)
	case memzModeTiles:
		paintMemzTiles(hdc)
	case memzModeRain:
		paintMemzRain(hdc)
	case memzModeNyan:
		paintMemzNyan(hdc)
	}
}

func paintMemzTiles(hdc uintptr) {
	width := memzSurface.width
	height := memzSurface.height

	procBitBlt.Call(hdc, 0, 0, uintptr(width), uintptr(height),
		memzSurface.memDC, 0, 0, srcCopy)

	for index := 0; index < 60; index++ {
		tileW := int32(48 + rand.Intn(140))
		tileH := int32(32 + rand.Intn(90))
		srcX := int32(rand.Intn(int(width - tileW)))
		srcY := int32(rand.Intn(int(height - tileH)))
		dstX := int32(rand.Intn(int(width - tileW)))
		dstY := int32(rand.Intn(int(height - tileH)))
		procBitBlt.Call(hdc, uintptr(dstX), uintptr(dstY), uintptr(tileW), uintptr(tileH),
			memzSurface.memDC, uintptr(srcX), uintptr(srcY), srcCopy)
	}
}

func paintMemzRain(hdc uintptr) {
	width := memzSurface.width
	height := memzSurface.height
	offset := memzSurface.rain % height

	procBitBlt.Call(hdc, 0, uintptr(offset), uintptr(width), uintptr(height-offset),
		memzSurface.memDC, 0, 0, srcCopy)
	procBitBlt.Call(hdc, 0, 0, uintptr(width), uintptr(offset),
		memzSurface.memDC, 0, uintptr(height-offset), srcCopy)

	for index := 0; index < 24; index++ {
		streak := memzRect{
			Left:   int32(rand.Intn(int(width))),
			Top:    int32(rand.Intn(int(height))),
			Right:  0,
			Bottom: 0,
		}
		streak.Right = streak.Left + int32(4+rand.Intn(18))
		streak.Bottom = streak.Top + int32(40+rand.Intn(180))
		brush, _, _ := procCreateSolidBrush.Call(uintptr(randomMemzColor()))
		if brush == 0 {
			continue
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&streak)), brush)
		procDeleteObject.Call(brush)
	}
}

func paintMemzNyan(hdc uintptr) {
	var full memzRect
	full.Right = memzSurface.width
	full.Bottom = memzSurface.height
	brush, _, _ := procCreateSolidBrush.Call(0x000000)
	if brush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&full)), brush)
		procDeleteObject.Call(brush)
	}

	x := memzSurface.nyanX
	y := memzSurface.nyanY
	rainbow := []uint32{0xFF0000, 0xFF9900, 0xFFFF00, 0x33CC33, 0x3399FF, 0x9933FF}
	for index, color := range rainbow {
		bar := memzRect{
			Left:   x - 220,
			Top:    y - 30 + int32(index*10),
			Right:  x - 10,
			Bottom: y - 22 + int32(index*10),
		}
		barBrush, _, _ := procCreateSolidBrush.Call(uintptr(color))
		if barBrush == 0 {
			continue
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&bar)), barBrush)
		procDeleteObject.Call(barBrush)
	}

	body := memzRect{Left: x, Top: y - 34, Right: x + 96, Bottom: y + 34}
	bodyBrush, _, _ := procCreateSolidBrush.Call(0x9C9C9C)
	if bodyBrush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&body)), bodyBrush)
		procDeleteObject.Call(bodyBrush)
	}

	for _, ear := range []memzRect{
		{Left: x + 6, Top: y - 46, Right: x + 26, Bottom: y - 32},
		{Left: x + 70, Top: y - 46, Right: x + 90, Bottom: y - 32},
	} {
		earRect := ear
		earBrush, _, _ := procCreateSolidBrush.Call(0x9C9C9C)
		if earBrush != 0 {
			procFillRect.Call(hdc, uintptr(unsafe.Pointer(&earRect)), earBrush)
			procDeleteObject.Call(earBrush)
		}
	}

	procEllipse.Call(hdc, uintptr(x+18), uintptr(y-10), uintptr(x+30), uintptr(y+2))
	procEllipse.Call(hdc, uintptr(x+66), uintptr(y-10), uintptr(x+78), uintptr(y+2))

	cheek, _, _ := procCreateSolidBrush.Call(0xFF77AA)
	if cheek != 0 {
		procEllipse.Call(hdc, uintptr(x+10), uintptr(y+8), uintptr(x+30), uintptr(y+22))
		procEllipse.Call(hdc, uintptr(x+66), uintptr(y+8), uintptr(x+86), uintptr(y+22))
		procDeleteObject.Call(cheek)
	}

	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, 0x000000)
	label := windows.StringToUTF16("NYAN")
	procTextOutW.Call(hdc, uintptr(x+30), uintptr(y+14),
		uintptr(unsafe.Pointer(&label[0])), uintptr(len(label)-1))
	runtime.KeepAlive(label)
}

func randomMemzColor() uint32 {
	return uint32(rand.Intn(0xFFFFFF))
}

func jitterCursorOnce() {
	var point memzPoint
	if result, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); result == 0 {
		return
	}
	point.X += int32(rand.Intn(9) - 4)
	point.Y += int32(rand.Intn(9) - 4)
	procSetCursorPos.Call(uintptr(point.X), uintptr(point.Y))
}

func openRandomURLs() {
	urls := []string{
		"https://es.wikipedia.org/wiki/Special:Random",
		"https://www.bing.com/search?q=memz",
		"https://archive.org",
		"https://www.youtube.com/results?search_query=nyan+cat",
		"https://example.com",
	}
	count := 2 + rand.Intn(2)
	for index := 0; index < count; index++ {
		target, targetBuf := memzUTF16(urls[rand.Intn(len(urls))])
		procShellExecuteW.Call(0, 0, uintptr(unsafe.Pointer(target)), 0, 0, 1)
		runtime.KeepAlive(targetBuf)
		time.Sleep(350 * time.Millisecond)
	}
}

func beepUntil(start time.Time, end time.Duration) {
	for time.Now().Before(start.Add(end)) {
		frequency := uint32(200 + rand.Intn(1200))
		duration := uint32(60 + rand.Intn(90))
		procBeep.Call(uintptr(frequency), uintptr(duration))
		time.Sleep(time.Duration(80+rand.Intn(200)) * time.Millisecond)
	}
}

func notepadType() {
	cmd := exec.Command("notepad.exe")
	if err := cmd.Start(); err != nil {
		return
	}

	memzNotepad = 0
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && memzNotepad == 0 {
		procEnumWindows.Call(memzEnumCallback(), 0)
		if memzNotepad == 0 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if memzNotepad == 0 {
		return
	}

	procShowWindow.Call(memzNotepad, swShow)
	procSetForegroundWindow.Call(memzNotepad)

	editClass, editBuf := memzUTF16("Edit")
	target := memzNotepad
	for attempt := 0; attempt < 12; attempt++ {
		child, _, _ := procFindWindowExW.Call(memzNotepad, 0, uintptr(unsafe.Pointer(editClass)), 0)
		if child != 0 {
			target = child
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	runtime.KeepAlive(editBuf)

	lines := []string{
		"MEMZ\r\n",
		"GoVoid was here\r\n",
		"OUT OF MEMORY\r\n",
		"0x" + strings.ToUpper(strconv.FormatInt(int64(rand.Uint32()), 16)) + "\r\n",
		"rebooting soon\r\n",
	}
	for _, line := range lines {
		for _, char := range line {
			procSendMessageW.Call(target, wmChar, uintptr(char), 1)
			time.Sleep(25 * time.Millisecond)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

func memzEnumCallback() uintptr {
	if memzEnumProc == 0 {
		memzEnumProc = syscall.NewCallback(memzEnumNotepad)
	}
	return memzEnumProc
}

func memzEnumNotepad(hwnd, lparam uintptr) uintptr {
	var buffer [64]uint16
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), 64)
	if windows.UTF16ToString(buffer[:]) == "Notepad" {
		memzNotepad = hwnd
		return 0
	}
	return 1
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
		time.Sleep(900 * time.Millisecond)
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

func memzUTF16(value string) (*uint16, []uint16) {
	buf := windows.StringToUTF16(value)
	return &buf[0], buf
}

func MemzDefaults() MemzOptions {
	return MemzOptions{
		Duration:        stageNyanEnd,
		MemoryCapMB:     512,
		RestartDelay:    12 * time.Second,
		WatchdogSeconds: 150,
	}
}

func MemzSummary(report MemzReport) string {
	var out strings.Builder
	fmt.Fprintf(&out, "etapas=%d memoria=%dMB tiempo=%dms reinicio=%v",
		len(report.Stages), report.PeakMB, report.Duration.Milliseconds(), report.RestartScheduled)
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
