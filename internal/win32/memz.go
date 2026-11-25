//go:build windows

package win32

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type MemzOptions struct {
	BoxInterval time.Duration
	MaxBoxes    int
}

type MemzReport struct {
	Phases   []string
	Boxes    int
	Duration time.Duration
	Stopped  bool
	Err      string
}

var (
	procMessageBoxW     = user32.NewProc("MessageBoxW")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procGetCurrentPID   = kernel32.NewProc("GetCurrentProcessId")
	procInvertRect      = user32.NewProc("InvertRect")
	procPeekMessageW    = user32.NewProc("PeekMessageW")
	procInvalidateRect  = user32.NewProc("InvalidateRect")
	procBeginPaint      = user32.NewProc("BeginPaint")
	procEndPaint        = user32.NewProc("EndPaint")
	procFillRect        = user32.NewProc("FillRect")
	procSetCursorPos    = user32.NewProc("SetCursorPos")
	procEnumWindows     = user32.NewProc("EnumWindows")
	procGetClassNameW   = user32.NewProc("GetClassNameW")
)

const (
	wmPaint    = 0x000F
	pmRemove   = 0x0001
	notSrcCopy = 0x00330008

	memzOverlayID = 1
	memzFrameMS   = 40

	memzModeIdle    = 0
	memzModeColors  = 1
	memzModeCorrupt = 2

	swHide         = 0
	swpNoActivate  = 0x0010
	wsExNoActivate = 0x08000000

	mbOK              = 0x00000000
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000

	phaseBoxesAt   = 8 * time.Second
	phaseColorsAt  = 16 * time.Second
	phaseCorruptAt = 24 * time.Second
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

type memzOverlay struct {
	hwnd     uintptr
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	previous uintptr
	width    int32
	height   int32
	mode     int32
	ready    bool
}

var (
	memzClassName = "GoVoidMemzWnd"
	memzCallback  uintptr
	memzOnce      sync.Once
	memzSurface   memzOverlay
	memzRunning   atomic.Bool
	memzStopCh    chan struct{}
	memzStopOnce  sync.Once

	memzColorSolid atomic.Bool
	memzColorValue atomic.Uint32

	memzBoxMu      sync.Mutex
	memzBoxMoved   = map[uintptr]struct{}{}
	memzBoxSlot    int
	memzBoxOpen    atomic.Int32
	memzBoxSpawned atomic.Int32

	memzScanMu   sync.Mutex
	memzScanList []uintptr
	memzEnumProc uintptr

	memzArabicWords = []string{
		"خطأ", "تحذير", "النظام", "الذاكرة", "القرص", "فشل", "تلف", "فيروس",
		"الملف", "العملية", "البيانات", "إعادة", "التشغيل", "غير", "كافية",
		"تم", "اكتشاف", "تعذر", "الوصول", "حرج", "خطير", "الرجاء", "الانتظار",
		"فادح", "مفقودة", "تالف", "المستخدم", "الجهاز", "البرنامج", "توقف",
		"قسم", "قطاع", "مجلد", "نسخ", "احتياطي", "استعادة", "تجاهل", "إلغاء",
	}
)

func RunMemz(opts MemzOptions) MemzReport {
	if !memzRunning.CompareAndSwap(false, true) {
		return MemzReport{Err: "ya hay una simulación en curso"}
	}
	defer memzRunning.Store(false)

	if opts.BoxInterval <= 0 {
		opts.BoxInterval = 900 * time.Millisecond
	}
	if opts.MaxBoxes <= 0 {
		opts.MaxBoxes = 160
	}

	report := MemzReport{}
	started := time.Now()

	memzStopCh = make(chan struct{})
	memzStopOnce = sync.Once{}
	stop := memzStopCh

	memzBoxMu.Lock()
	memzBoxMoved = map[uintptr]struct{}{}
	memzBoxSlot = 0
	memzBoxMu.Unlock()
	memzBoxOpen.Store(0)
	memzBoxSpawned.Store(0)
	memzColorSolid.Store(false)

	uiStop := make(chan struct{})
	ready := make(chan struct{})
	finished := make(chan struct{})

	go runMemzUI(uiStop, ready, finished)
	select {
	case <-ready:
	case <-time.After(4 * time.Second):
		report.Err = "overlay no disponible"
	}

	phase := func(name string) { report.Phases = append(report.Phases, name) }

	phase("cursor")
	go cursorStorm(stop)
	go positionMemzBoxes(stop)

	boxesAt := started.Add(phaseBoxesAt)
	colorsAt := started.Add(phaseColorsAt)
	corruptAt := started.Add(phaseCorruptAt)

	boxesOn := false
	colorsOn := false
	corruptOn := false

loop:
	for {
		select {
		case <-stop:
			break loop
		default:
		}

		now := time.Now()
		if !boxesOn && !now.Before(boxesAt) {
			boxesOn = true
			phase("mensajes")
			go boxStorm(stop, opts.BoxInterval, opts.MaxBoxes)
		}
		if !colorsOn && !now.Before(colorsAt) {
			colorsOn = true
			phase("colores")
			captureDesktop()
			setMemzMode(memzModeColors)
			go colorStorm(stop)
		}
		if !corruptOn && !now.Before(corruptAt) {
			corruptOn = true
			phase("corrupcion")
			captureDesktop()
			setMemzMode(memzModeCorrupt)
		}

		time.Sleep(60 * time.Millisecond)
	}

	close(uiStop)
	<-finished
	closeMemzBoxes()

	report.Stopped = true
	report.Boxes = int(memzBoxSpawned.Load())
	report.Duration = time.Since(started)
	return report
}

func StopMemz() error {
	if !memzRunning.Load() {
		return errors.New("no hay una simulación en marcha")
	}
	if memzStopCh != nil {
		memzStopOnce.Do(func() { close(memzStopCh) })
	}
	closeMemzBoxes()
	return nil
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
	case wmTimer:
		if wparam == memzOverlayID {
			procInvalidateRect.Call(hwnd, 0, 0)
		}
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
	case memzModeColors:
		paintMemzColors(hdc)
	case memzModeCorrupt:
		paintMemzCorrupt(hdc)
	}
}

func paintMemzColors(hdc uintptr) {
	if memzColorSolid.Load() {
		var full memzRect
		full.Right = memzSurface.width
		full.Bottom = memzSurface.height
		brush, _, _ := procCreateSolidBrush.Call(uintptr(memzColorValue.Load()))
		if brush == 0 {
			return
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&full)), brush)
		procDeleteObject.Call(brush)
		return
	}

	procBitBlt.Call(hdc, 0, 0,
		uintptr(memzSurface.width), uintptr(memzSurface.height),
		memzSurface.memDC, 0, 0, notSrcCopy)
}

func paintMemzCorrupt(hdc uintptr) {
	width := memzSurface.width
	height := memzSurface.height

	procBitBlt.Call(hdc, 0, 0, uintptr(width), uintptr(height),
		memzSurface.memDC, 0, 0, srcCopy)

	for index := 0; index < 48; index++ {
		band := int32(3 + rand.Intn(26))
		y := int32(rand.Intn(int(height)))
		dx := int32(rand.Intn(200) - 100)
		procBitBlt.Call(hdc, uintptr(dx), uintptr(y),
			uintptr(width), uintptr(band),
			memzSurface.memDC, 0, uintptr(y), srcCopy)
	}

	for index := 0; index < 36; index++ {
		block := memzRect{
			Left:   int32(rand.Intn(int(width))),
			Top:    int32(rand.Intn(int(height))),
			Right:  0,
			Bottom: 0,
		}
		block.Right = block.Left + int32(12+rand.Intn(180))
		block.Bottom = block.Top + int32(8+rand.Intn(90))
		brush, _, _ := procCreateSolidBrush.Call(uintptr(randomGlitchColor()))
		if brush == 0 {
			continue
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&block)), brush)
		procDeleteObject.Call(brush)
	}

	for index := 0; index < 12; index++ {
		patch := memzRect{
			Left:   int32(rand.Intn(int(width))),
			Top:    int32(rand.Intn(int(height))),
			Right:  0,
			Bottom: 0,
		}
		patch.Right = patch.Left + int32(20+rand.Intn(260))
		patch.Bottom = patch.Top + int32(10+rand.Intn(120))
		procInvertRect.Call(hdc, uintptr(unsafe.Pointer(&patch)))
	}

	for index := 0; index < 90; index++ {
		y := int32(rand.Intn(int(height)))
		line := memzRect{
			Left:   0,
			Top:    y,
			Right:  width,
			Bottom: y + int32(1+rand.Intn(3)),
		}
		brush, _, _ := procCreateSolidBrush.Call(uintptr(randomGlitchColor()))
		if brush == 0 {
			continue
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&line)), brush)
		procDeleteObject.Call(brush)
	}
}

func randomGlitchColor() uint32 {
	base := rand.Intn(3)
	switch base {
	case 0:
		return uint32(rand.Intn(0x40))
	case 1:
		return 0xC0000000 | uint32(rand.Intn(0x3FFFFF))
	default:
		return uint32(rand.Intn(0xFFFFFF))
	}
}

func cursorStorm(stop <-chan struct{}) {
	vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	vw, _, _ := procGetSystemMetrics.Call(smCxVirtualScreen)
	vh, _, _ := procGetSystemMetrics.Call(smCyVirtualScreen)
	if vw == 0 || vh == 0 {
		vw, _, _ = procGetSystemMetrics.Call(smCXScreen)
		vh, _, _ = procGetSystemMetrics.Call(smCYScreen)
	}
	if vw == 0 || vh == 0 {
		return
	}

	left := int(int32(vx))
	top := int(int32(vy))
	width := int(vw)
	height := int(vh)

	for {
		select {
		case <-stop:
			return
		default:
		}

		x := left + rand.Intn(width)
		y := top + rand.Intn(height)
		procSetCursorPos.Call(uintptr(x), uintptr(y))
		time.Sleep(time.Duration(50+rand.Intn(90)) * time.Millisecond)
	}
}

func colorStorm(stop <-chan struct{}) {
	tick := 0
	for {
		select {
		case <-stop:
			return
		default:
		}

		tick++
		if tick%3 == 0 {
			memzColorValue.Store(randomGlitchColor())
			memzColorSolid.Store(true)
		} else {
			memzColorSolid.Store(false)
		}
		time.Sleep(700 * time.Millisecond)
	}
}

func boxStorm(stop <-chan struct{}, interval time.Duration, max int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if int(memzBoxOpen.Load()) >= max {
				continue
			}
			spawnMemzBox()
		}
	}
}

func spawnMemzBox() {
	title := memzArabicLine(2)
	body := memzArabicLine(6) + "\n\n0x" + strings.ToUpper(fmt.Sprintf("%08X", rand.Uint32())) + "\n" + memzArabicLine(3)

	memzBoxSpawned.Add(1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		titlePtr, titleBuf := memzUTF16(title)
		bodyPtr, bodyBuf := memzUTF16(body)
		procMessageBoxW.Call(
			0,
			uintptr(unsafe.Pointer(bodyPtr)),
			uintptr(unsafe.Pointer(titlePtr)),
			uintptr(mbOK|mbIconInformation|mbSetForeground|mbTopmost),
		)
		runtime.KeepAlive(titleBuf)
		runtime.KeepAlive(bodyBuf)
	}()
}

func memzArabicLine(words int) string {
	if words < 1 {
		words = 1
	}
	parts := make([]string, words)
	for index := range parts {
		parts[index] = memzArabicWords[rand.Intn(len(memzArabicWords))]
	}
	return strings.Join(parts, " ")
}

func positionMemzBoxes(stop <-chan struct{}) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			boxes := scanMemzBoxes()
			seen := make(map[uintptr]struct{}, len(boxes))

			memzBoxMu.Lock()
			for _, hwnd := range boxes {
				seen[hwnd] = struct{}{}
				if _, ok := memzBoxMoved[hwnd]; ok {
					continue
				}
				x, y := memzBoxSlotPos(memzBoxSlot)
				memzBoxSlot++
				procSetWindowPos.Call(hwnd, hwndTopmost,
					uintptr(x), uintptr(y), 0, 0,
					swpNoSize|swpNoActivate|swpShowWindow)
				memzBoxMoved[hwnd] = struct{}{}
			}
			for hwnd := range memzBoxMoved {
				if _, ok := seen[hwnd]; !ok {
					delete(memzBoxMoved, hwnd)
				}
			}
			memzBoxMu.Unlock()

			memzBoxOpen.Store(int32(len(boxes)))
		}
	}
}

func memzBoxSlotPos(slot int) (int, int) {
	width, _, _ := procGetSystemMetrics.Call(smCXScreen)
	height, _, _ := procGetSystemMetrics.Call(smCYScreen)

	spanX := int(width) - 340
	if spanX < 140 {
		spanX = 140
	}
	spanY := int(height) - 210
	if spanY < 120 {
		spanY = 120
	}

	x := 24 + (slot*46)%spanX
	y := 24 + (slot*38)%spanY
	return x, y
}

func scanMemzBoxes() []uintptr {
	memzScanMu.Lock()
	defer memzScanMu.Unlock()

	memzScanList = memzScanList[:0]
	procEnumWindows.Call(memzEnumCallback(), 0)
	return append([]uintptr(nil), memzScanList...)
}

func memzEnumCallback() uintptr {
	if memzEnumProc == 0 {
		memzEnumProc = syscall.NewCallback(memzEnumBoxCallback)
	}
	return memzEnumProc
}

func memzEnumBoxCallback(hwnd, lparam uintptr) uintptr {
	var buffer [64]uint16
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), 64)
	if windows.UTF16ToString(buffer[:]) != "#32770" {
		return 1
	}
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return 1
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != currentProcessID() {
		return 1
	}
	memzScanList = append(memzScanList, hwnd)
	return 1
}

func currentProcessID() uint32 {
	result, _, _ := procGetCurrentPID.Call()
	return uint32(result)
}

func closeMemzBoxes() {
	for _, hwnd := range scanMemzBoxes() {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
}

func memzUTF16(value string) (*uint16, []uint16) {
	buf := windows.StringToUTF16(value)
	return &buf[0], buf
}

func MemzDefaults() MemzOptions {
	return MemzOptions{
		BoxInterval: 900 * time.Millisecond,
		MaxBoxes:    160,
	}
}

func MemzSummary(report MemzReport) string {
	var out strings.Builder
	fmt.Fprintf(&out, "fases=%d mensajes=%d tiempo=%dms detenido=%v",
		len(report.Phases), report.Boxes, report.Duration.Milliseconds(), report.Stopped)
	if report.Err != "" {
		fmt.Fprintf(&out, " error=%s", report.Err)
	}
	return out.String()
}
