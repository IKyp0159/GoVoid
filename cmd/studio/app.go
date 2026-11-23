//go:build windows

package main

// Ventana de GoVoid Studio. Una sola pantalla: tres credenciales, un botón.
// Todo lo demás —identificador, nombre, ejecutable, tareas, servicio, carpeta
// y paquete— se deriva de esos tres datos.
//
// Todo el estado vive en App y solo se toca desde el hilo de la ventana. Los
// trabajos largos corren en goroutine y vuelven por post().

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/local/govoid/internal/kit"
)

const (
	appTitle   = "GoVoid Studio"
	appVersion = "1.0"

	idFileOpen = 100 + iota
	idFileSave
	idFileExit

	idToolsCheck
	idToolsBuild
	idToolsOpenOut
	idToolsInvite
	idToolsPortal
	idToolsCopyLog
	idToolsSaveLog

	idHelpAbout

	idBtnBuild = 500 + iota
	idBtnCheck
	idBtnOpenOut
	idChkShowToken
)

// Identificadores que devuelve MessageBox.
const (
	idOk     = 1
	idYes    = 6
	idNo     = 7
	idCancel = 2
)

// Medidas en unidades de 96 ppp.
const (
	headerH = 104
	margin  = 28
	fieldH  = 32
	groupH  = 88
	stepW   = 34

	panelTop    = headerH + 14
	panelBottom = summaryY - 12
)

// Colores de la marca, en el orden que espera COLORREF.
var (
	colorInk    = rgb(17, 24, 39)    // #111827
	colorPanel  = rgb(241, 245, 249) // #F1F5F9
	colorEdge   = rgb(226, 232, 240) // #E2E8F0
	colorAccent = rgb(16, 185, 129)  // #10B981
	colorMuted  = rgb(107, 114, 128) // #6B7280
	colorBody   = rgb(31, 41, 55)    // #1F2937
	colorLog    = rgb(11, 18, 32)    // #0B1220
	colorLogInk = rgb(209, 213, 219) // #D1D5DB
)

type ctrl struct {
	hwnd uintptr
	x, y int32
	w, h int32
}

// App es la ventana y su estado. Un único ejemplar por proceso.
type App struct {
	hwnd     uintptr
	status   uintptr
	progress uintptr
	log      uintptr
	instance uintptr
	accel    uintptr

	headerTitle uintptr
	headerSub   uintptr
	headerMeta  uintptr
	headerBrush uintptr
	panelBrush  uintptr
	panelEdge   uintptr
	logBrush    uintptr
	stepFont    uintptr
	greenText   map[uintptr]bool

	dpi      int
	font     uintptr
	fontBold uintptr
	fontMono uintptr
	fontBig  uintptr

	controls []*ctrl
	fields   map[string]uintptr
	checks   map[string]uintptr
	buttons  map[int]uintptr
	hints    map[uintptr]bool
	onPanel  map[uintptr]bool

	busy        bool
	cancelFn    func()
	canceled    bool
	clamping    bool
	forceShared bool
	summary     uintptr
	lastOut     string

	queue struct {
		sync.Mutex
		items []func()
	}
}

var (
	app         *App
	wndProcAddr uintptr
)

func newApp() *App {
	return &App{
		fields:    make(map[string]uintptr),
		checks:    make(map[string]uintptr),
		buttons:   make(map[int]uintptr),
		hints:     make(map[uintptr]bool),
		onPanel:   make(map[uintptr]bool),
		greenText: make(map[uintptr]bool),
	}
}

func (a *App) scale(value int32) int32 {
	return value * int32(a.dpi) / 96
}

// post encola trabajo para el hilo de la interfaz. Seguro desde cualquier
// goroutine: no toca ventanas, solo la cola y un PostMessage.
func (a *App) post(fn func()) {
	a.queue.Lock()
	a.queue.items = append(a.queue.items, fn)
	a.queue.Unlock()
	procPostMessage.Call(a.hwnd, wmAppRun, 0, 0)
}

func (a *App) drain() {
	for {
		a.queue.Lock()
		if len(a.queue.items) == 0 {
			a.queue.Unlock()
			return
		}
		batch := a.queue.items
		a.queue.items = nil
		a.queue.Unlock()
		for _, fn := range batch {
			fn()
		}
	}
}

// --- arranque ---

func runApp() error {
	dpiAware()

	icc := initCommonControlsEx{size: uint32(unsafe.Sizeof(initCommonControlsEx{})), icc: iccBar | iccProgress}
	if ok, _, _ := procInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc))); ok == 0 {
		return fmt.Errorf("InitCommonControlsEx falló")
	}

	a := newApp()
	app = a
	a.dpi = windowDPI(0)
	a.instance, _, _ = procGetModuleHandle.Call(0)

	wndProcAddr = syscall.NewCallback(a.wndProc)

	className := u16("GoVoidStudioWindow")
	icon, _, _ := procLoadIcon.Call(a.instance, 1)
	if icon == 0 {
		icon, _, _ = procLoadIcon.Call(0, idiApplication)
	}
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)

	class := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		style:         0x0003, // CS_HREDRAW | CS_VREDRAW
		lpfnWndProc:   wndProcAddr,
		hInstance:     a.instance,
		hIcon:         icon,
		hCursor:       cursor,
		hbrBackground: colorWindow + 1,
		lpszClassName: className,
		hIconSm:       icon,
	}
	if atom, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return fmt.Errorf("RegisterClassEx falló: %v", err)
	}

	clientW, clientH := a.scale(900), a.scale(780)
	style := uint32(wsOverlappedWindow | wsClipChildren)
	var frame rect
	frame.right, frame.bottom = clientW, clientH
	if err := procAdjustWindowRectExFor.Find(); err == nil {
		procAdjustWindowRectExFor.Call(uintptr(unsafe.Pointer(&frame)), uintptr(style), 0, 0, uintptr(a.dpi))
	} else {
		procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&frame)), uintptr(style), 0, 0)
	}
	winW := frame.right - frame.left
	winH := frame.bottom - frame.top

	workX, workY, workW, workH := workArea()
	x := workX + (workW-winW)/2
	y := workY + (workH-winH)/3

	hwnd, _, err := procCreateWindowEx.Call(
		0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(u16(appTitle))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(winW), uintptr(winH),
		0, 0, a.instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx falló: %v", err)
	}
	a.hwnd = hwnd

	procShowWindow.Call(hwnd, swShow)
	procSetFocus.Call(hwnd)

	accels := []accel{
		{fVirt: fvirtKey | fcontrol, key: 'O', cmd: idFileOpen},
		{fVirt: fvirtKey | fcontrol, key: 'S', cmd: idFileSave},
		{fVirt: fvirtKey, key: vkF5, cmd: idToolsCheck},
		{fVirt: fvirtKey, key: vkF9, cmd: idToolsBuild},
	}
	a.accel, _, _ = procCreateAcceleratorTable.Call(uintptr(unsafe.Pointer(&accels[0])), uintptr(len(accels)))

	var message msg
	for {
		result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			break
		}
		if a.accel != 0 {
			if handled, _, _ := procTranslateAccelerator.Call(a.hwnd, a.accel, uintptr(unsafe.Pointer(&message))); handled != 0 {
				continue
			}
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}

	if a.accel != 0 {
		procDeleteObject.Call(a.accel)
	}
	return nil
}

func workArea() (x, y, w, h int32) {
	var area rect
	if ok, _, _ := procSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&area)), 0); ok == 0 {
		return 0, 0, int32(systemMetric(0)), int32(systemMetric(1))
	}
	return area.left, area.top, area.right - area.left, area.bottom - area.top
}

func systemMetric(index int32) int {
	value, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(value))
}

// --- procedimiento de ventana ---

func (a *App) wndProc(hwnd, message, wparam, lparam uintptr) (result uintptr) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// Un panic aquí mataría el bucle de mensajes: la ventana seguiría
			// en pantalla pero muerta. Se registra y la app sigue, para que el
			// fallo quede a la vista en lugar de desaparecer.
			a.logLine("fallo interno en la ventana: %v", recovered)
			result = 0
		}
	}()

	switch message {
	case wmCreate:
		a.hwnd = hwnd
		a.onCreate()
		return 0

	case wmSize:
		a.clampSize()
		a.layout()
		if a.status != 0 {
			procSendMessage.Call(a.status, wmSize, 0, 0)
		}
		return 0

	case wmPaint:
		a.onPaint()
		return 0

	case wmCommand:
		a.onCommand(loword(wparam), hiword(wparam), lparam)
		return 0

	case wmAppRun:
		a.drain()
		return 0

	case wmTimer:
		if wparam == 1 {
			procKillTimer.Call(a.hwnd, 1)
			procPostMessage.Call(a.hwnd, wmClose, 0, 0)
		}
		return 0

	case wmCtlColorStatic:
		hdc := wparam
		procSetBkMode.Call(hdc, 1) // TRANSPARENT
		if lparam == a.headerTitle {
			procSetTextColor.Call(hdc, rgb(255, 255, 255))
			return a.headerBrush
		}
		if lparam == a.headerSub {
			procSetTextColor.Call(hdc, rgb(148, 163, 184))
			return a.headerBrush
		}
		if lparam == a.headerMeta {
			procSetTextColor.Call(hdc, rgb(100, 116, 139))
			return a.headerBrush
		}
		if lparam == a.log {
			procSetTextColor.Call(hdc, colorLogInk)
			procSetBkColor.Call(hdc, colorLog)
			return a.logBrush
		}
		// Solo los controles que viven dentro del panel claro lo usan de fondo.
		if a.onPanel[lparam] {
			procSetBkColor.Call(hdc, colorPanel)
			switch {
			case a.greenText[lparam]:
				procSetTextColor.Call(hdc, colorAccent)
			case a.hints[lparam]:
				procSetTextColor.Call(hdc, colorMuted)
			default:
				procSetTextColor.Call(hdc, colorInk)
			}
			return a.panelBrush
		}
		procSetTextColor.Call(hdc, colorMuted)
		return sysColorBrush(colorWindow)

	case wmCtlColorBtn:
		if lparam == a.checks["showToken"] {
			procSetBkColor.Call(wparam, colorPanel)
			return a.panelBrush
		}
		return 0

	case wmCtlColorEdit:
		hdc := wparam
		procSetTextColor.Call(hdc, rgb(17, 24, 39))
		procSetBkColor.Call(hdc, rgb(255, 255, 255))
		return sysColorBrush(colorWindow)

	case wmDpiChanged:
		a.dpi = int(hiword(wparam))
		if a.dpi < 96 {
			a.dpi = 96
		}
		a.rebuildFonts()
		a.layout()
		return 0

	case wmClose:
		a.onClose()
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	return defWindowProc(hwnd, message, wparam, lparam)
}

func defWindowProc(hwnd, message, wparam, lparam uintptr) uintptr {
	result, _, _ := procDefWindowProc.Call(hwnd, message, wparam, lparam)
	return result
}

func rgb(r, g, b uint32) uintptr {
	return uintptr(r | g<<8 | b<<16)
}

// clampSize mantiene la ventana por encima del mínimo sin tocar LPARAM.
func (a *App) clampSize() {
	if a.clamping {
		return
	}
	var bounds rect
	procGetWindowRect.Call(a.hwnd, uintptr(unsafe.Pointer(&bounds)))
	width := bounds.right - bounds.left
	height := bounds.bottom - bounds.top
	minW, minH := a.scale(820), a.scale(680)
	if width >= minW && height >= minH {
		return
	}
	if width < minW {
		width = minW
	}
	if height < minH {
		height = minH
	}
	a.clamping = true
	procSetWindowPos.Call(a.hwnd, 0, 0, 0, uintptr(width), uintptr(height), swpNoMove|swpNoZOrder)
	a.clamping = false
}

// --- pintura ---

func (a *App) onPaint() {
	var paint paintStruct
	hdc, _, _ := procBeginPaint.Call(a.hwnd, uintptr(unsafe.Pointer(&paint)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(a.hwnd, uintptr(unsafe.Pointer(&paint)))

	var client rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&client)))

	band := rect{0, 0, client.right, a.scale(headerH)}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&band)), a.headerBrush)

	accentBrush, _, _ := procCreateSolidBrush.Call(rgb(16, 185, 129))
	if accentBrush != 0 {
		line := rect{0, a.scale(headerH), client.right, a.scale(headerH) + a.scale(4)}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&line)), accentBrush)
		procDeleteObject.Call(accentBrush)
	}

	// Panel de credenciales: el bloque que se rellena. Los tres campos viven
	// encima, así que solo se pinta el fondo y el filo.
	panel := rect{
		a.scale(margin - 12), a.scale(panelTop),
		client.right - a.scale(margin-12), a.scale(panelBottom),
	}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&panel)), a.panelBrush)
	procFrameRect.Call(hdc, uintptr(unsafe.Pointer(&panel)), a.panelEdge)
}

// --- construcción de la interfaz ---

func (a *App) onCreate() {
	a.rebuildFonts()
	a.headerBrush, _, _ = procCreateSolidBrush.Call(rgb(17, 24, 39))
	a.panelBrush, _, _ = procCreateSolidBrush.Call(rgb(241, 245, 249))
	a.panelEdge, _, _ = procCreateSolidBrush.Call(rgb(226, 232, 240))
	a.logBrush, _, _ = procCreateSolidBrush.Call(colorLog)

	a.buildMenu()
	a.buildHeader()
	a.buildFields()
	a.buildActions()
	a.buildStatus()
	a.addLog()
	a.logLine("GoVoid Studio %s · %s", appVersion, runtime.Version())
	a.logLine("Repositorio: %s", a.repoOrHint())
	a.loadSettings()
	a.layout()
	a.updateSummary()
	a.setStatus("Listo. Pega el token, el id de la guild y el webhook.")
	procSetFocus.Call(a.fields["token"])

	if smoke {
		writeSmokeReport(os.Getenv("GOVOID_SMOKE_OUT"))
		procSetTimer.Call(a.hwnd, 1, 2500, 0)
	}
}

func (a *App) rebuildFonts() {
	for _, font := range []uintptr{a.font, a.fontBold, a.fontMono, a.fontBig, a.stepFont} {
		if font != 0 {
			procDeleteObject.Call(font)
		}
	}
	a.font = a.makeFont("Segoe UI", 9, 400)
	a.fontBold = a.makeFont("Segoe UI Semibold", 9, 600)
	a.fontMono = a.makeFont("Consolas", 9, 400)
	a.fontBig = a.makeFont("Segoe UI Semibold", 19, 600)
	a.stepFont = a.makeFont("Consolas", 11, 700)

	for _, control := range a.controls {
		font := a.font
		if control.hwnd == a.log {
			font = a.fontMono
		}
		if control.hwnd == a.headerTitle {
			font = a.fontBig
		}
		procSendMessage.Call(control.hwnd, 0x0030, font, 1)
	}
}

func (a *App) makeFont(face string, points int32, weight int32) uintptr {
	height := -points * int32(a.dpi) / 72
	font, _, _ := procCreateFont.Call(
		uintptr(height), 0, 0, 0, uintptr(weight),
		0, 0, 0, 1 /* DEFAULT_CHARSET */, 0, 0, 5 /* CLEARTYPE_QUALITY */, 0,
		uintptr(unsafe.Pointer(u16(face))))
	return font
}

func (a *App) buildMenu() {
	file := mustMenu(procCreatePopupMenu)
	appendMenu(file, idFileOpen, "Abrir configuración...\tCtrl+O")
	appendMenu(file, idFileSave, "Guardar configuración\tCtrl+S")
	appendMenu(file, 0, "")
	appendMenu(file, idFileExit, "Salir")

	tools := mustMenu(procCreatePopupMenu)
	appendMenu(tools, idToolsCheck, "Probar conexión\tF5")
	appendMenu(tools, idToolsBuild, "Compilar agente\tF9")
	appendMenu(tools, 0, "")
	appendMenu(tools, idToolsOpenOut, "Abrir carpeta de salida")
	appendMenu(tools, idToolsInvite, "Copiar invitación del bot")
	appendMenu(tools, idToolsPortal, "Abrir portal de Discord")
	appendMenu(tools, 0, "")
	appendMenu(tools, idToolsCopyLog, "Copiar actividad")
	appendMenu(tools, idToolsSaveLog, "Guardar actividad...")

	help := mustMenu(procCreatePopupMenu)
	appendMenu(help, idHelpAbout, "Acerca de GoVoid Studio")

	main := mustMenu(procCreateMenu)
	appendPopup(main, file, "Archivo")
	appendPopup(main, tools, "Herramientas")
	appendPopup(main, help, "Ayuda")
	procSetMenu.Call(a.hwnd, main)
	procDrawMenuBar.Call(a.hwnd)
}

func mustMenu(proc *windows.LazyProc) uintptr {
	menu, _, _ := proc.Call()
	return menu
}

func appendMenu(menu uintptr, id int, text string) {
	const mfString = 0x0000
	const mfSeparator = 0x0800
	if text == "" {
		procAppendMenu.Call(menu, mfSeparator, 0, 0)
		return
	}
	procAppendMenu.Call(menu, mfString, uintptr(id), uintptr(unsafe.Pointer(u16(text))))
}

func appendPopup(menu, popup uintptr, text string) {
	const mfPopup = 0x0010
	procAppendMenu.Call(menu, mfPopup, popup, uintptr(unsafe.Pointer(u16(text))))
}

func (a *App) buildHeader() {
	a.headerTitle = a.newControl("STATIC", "GoVoid Studio", wsChild|ssLeft, 0, 0, 500, 38, 0)
	a.headerSub = a.newControl("STATIC", "Constructor de agentes · un bot, un servidor, un ejecutable", wsChild|ssLeft, 0, 0, 700, 20, 0)
	procSendMessage.Call(a.headerTitle, 0x0030, a.fontBig, 1)

	a.headerMeta = a.newControl("STATIC", "v"+appVersion+"  ·  local  ·  sin CGO", wsChild|ssRight, 0, 0, 0, 18, 0)
}

func (a *App) buildFields() {
	a.addGroup(0, "TOKEN DEL BOT", "token",
		"El token de la aplicación de Discord. Se guarda cifrado con DPAPI en este equipo.", true)
	a.addGroup(1, "ID DE LA GUILD", "guild",
		"El servidor donde el agente abrirá su canal. Clic derecho en el servidor → Copiar ID.", false)
	a.addGroup(2, "URL DEL WEBHOOK", "webhook",
		"Canal de avisos: Ajustes del canal → Integraciones → Webhooks → Copiar URL.", false)

	a.addCheck("showToken", "Mostrar token", 0, 0, false)

	a.summary = a.newControl("STATIC", "", wsChild|ssLeft, 0, 0, 800, 20, 0)
	a.hints[a.summary] = true
	a.onPanel[a.summary] = true
}

// newControl crea un control hijo y guarda su rectángulo lógico.
func (a *App) newControl(class, text string, style uint32, x, y, w, h int32, id int) uintptr {
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(u16(class))),
		uintptr(unsafe.Pointer(u16(text))), uintptr(style|wsChild|wsVisible),
		0, 0, 0, 0, a.hwnd, uintptr(id), a.instance, 0)
	procSendMessage.Call(hwnd, 0x0030, a.font, 1)
	a.controls = append(a.controls, &ctrl{hwnd: hwnd, x: x, y: y, w: w, h: h})
	return hwnd
}

// addGroup pinta una credencial: número, rótulo, campo y ayuda. El número es
// el único adorno: dice que solo hay tres cosas que rellenar.
func (a *App) addGroup(index int, label, key, hint string, secret bool) {
	y := int32(headerH + margin + index*groupH)

	step := a.newControl("STATIC", fmt.Sprintf("%02d", index+1), wsChild|ssLeft, 0, 0, 28, 20, 0)
	procSendMessage.Call(step, 0x0030, a.stepFont, 1)
	a.controls[len(a.controls)-1].x, a.controls[len(a.controls)-1].y = margin, y-2
	a.greenText[step] = true
	a.onPanel[step] = true

	a.newControl("STATIC", label, wsChild|ssLeft, 0, 0, 400, 18, 0)
	labelControl := a.controls[len(a.controls)-1]
	labelControl.x, labelControl.y = margin+stepW, y
	procSendMessage.Call(labelControl.hwnd, 0x0030, a.fontBold, 1)
	a.onPanel[labelControl.hwnd] = true

	a.addEdit(key, margin+stepW, y+22, 0, fieldH, secret)

	help := a.newControl("STATIC", hint, wsChild|ssLeft, 0, 0, 800, 16, 0)
	helpControl := a.controls[len(a.controls)-1]
	helpControl.x, helpControl.y = margin+stepW, y+58
	a.hints[help] = true
	a.onPanel[help] = true
}

func (a *App) addEdit(key string, x, y, w, h int32, secret bool) uintptr {
	style := uint32(esLeft | esAutoHScroll)
	if secret {
		style |= esPassword
	}
	hwnd, _, _ := procCreateWindowEx.Call(wsExClientEdge, uintptr(unsafe.Pointer(u16("EDIT"))),
		uintptr(unsafe.Pointer(u16(""))), uintptr(style|wsChild|wsVisible|wsTabStop),
		0, 0, 0, 0, a.hwnd, 0, a.instance, 0)
	procSendMessage.Call(hwnd, 0x0030, a.font, 1)
	a.controls = append(a.controls, &ctrl{hwnd: hwnd, x: x, y: y, w: w, h: h})
	a.fields[key] = hwnd
	return hwnd
}

func (a *App) addCheck(key, text string, x, y int32, checked bool) uintptr {
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(u16("BUTTON"))),
		uintptr(unsafe.Pointer(u16(text))), wsChild|wsVisible|wsTabStop|bsAutoCheckBox,
		0, 0, 0, 0, a.hwnd, uintptr(idChkShowToken), a.instance, 0)
	procSendMessage.Call(hwnd, 0x0030, a.font, 1)
	a.controls = append(a.controls, &ctrl{hwnd: hwnd, x: x, y: y, w: 120, h: 18})
	a.checks[key] = hwnd
	if checked {
		procSendMessage.Call(hwnd, 0x00F1, 1, 0)
	}
	return hwnd
}

func (a *App) buildActions() {
	a.progress, _, _ = procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(u16("msctls_progress32"))), 0,
		wsChild, 0, 0, 0, 0, a.hwnd, 0, a.instance, 0)
	procSendMessage.Call(a.progress, pbmSetRange32, 0, 1000)
	procSendMessage.Call(a.progress, pbmSetPos, 0, 0)
	procShowWindow.Call(a.progress, 0)

	a.addButton(idBtnOpenOut, "Abrir carpeta", 140)
	a.addButton(idBtnCheck, "Probar conexión", 160)
	a.addButton(idBtnBuild, "Compilar agente", 200)
}

func (a *App) addButton(id int, text string, width int32) uintptr {
	style := uint32(wsChild | wsVisible | wsTabStop | bsPushButton)
	if id == idBtnBuild {
		style = wsChild | wsVisible | wsTabStop | 0x0001 // BS_DEFPUSHBUTTON
	}
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(u16("BUTTON"))),
		uintptr(unsafe.Pointer(u16(text))), uintptr(style),
		0, 0, 0, 0, a.hwnd, uintptr(id), a.instance, 0)
	procSendMessage.Call(hwnd, 0x0030, a.font, 1)
	a.controls = append(a.controls, &ctrl{hwnd: hwnd, w: width, h: 36})
	a.buttons[id] = hwnd
	return hwnd
}

func (a *App) buildStatus() {
	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(u16("msctls_statusbar32"))), 0,
		wsChild|wsVisible|0x0100, /* SBARS_SIZEGRIP */
		0, 0, 0, 0, a.hwnd, 0, a.instance, 0)
	a.status = hwnd
	procSendMessage.Call(hwnd, 0x0030, a.font, 1)
	parts := []int32{-1}
	procSendMessage.Call(hwnd, sbSetParts, 1, uintptr(unsafe.Pointer(&parts[0])))
}

// addLog crea el cuadro de actividad. Va debajo de los botones y ocupa el
// espacio que sobre. Fondo oscuro: el registro es la única zona que se lee
// como una consola y así se separa del formulario.
func (a *App) addLog() {
	caption := a.newControl("STATIC", "ACTIVIDAD", wsChild|ssLeft, margin, logTop, 300, 14, 0)
	procSendMessage.Call(caption, 0x0030, a.fontBold, 1)
	a.hints[caption] = true

	a.log, _, _ = procCreateWindowEx.Call(wsExClientEdge, uintptr(unsafe.Pointer(u16("EDIT"))),
		uintptr(unsafe.Pointer(u16(""))),
		wsChild|wsVisible|wsTabStop|wsVScroll|esMultiline|esReadOnly|esWantReturn|esNoHideSel,
		0, 0, 0, 0, a.hwnd, 0, a.instance, 0)
	procSendMessage.Call(a.log, 0x0030, a.fontMono, 1)
	a.controls = append(a.controls, &ctrl{hwnd: a.log, x: margin, y: logTop + 20, w: 0, h: 0})
}

// --- disposición ---

const (
	summaryY = headerH + margin + 3*groupH + 4
	actionsY = summaryY + 34
	logTop   = actionsY + 56
)

func (a *App) layout() {
	if a.hwnd == 0 {
		return
	}
	var client rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&client)))
	width := client.right - client.left
	height := client.bottom - client.top

	contentW := width - 2*a.scale(margin)

	if a.headerTitle != 0 {
		procMoveWindow.Call(a.headerTitle, uintptr(a.scale(margin)), uintptr(a.scale(22)),
			uintptr(a.scale(500)), uintptr(a.scale(38)), 1)
		procMoveWindow.Call(a.headerSub, uintptr(a.scale(margin)), uintptr(a.scale(64)),
			uintptr(a.scale(700)), uintptr(a.scale(22)), 1)
		procMoveWindow.Call(a.headerMeta,
			uintptr(width-a.scale(margin)-a.scale(240)), uintptr(a.scale(30)),
			uintptr(a.scale(240)), uintptr(a.scale(18)), 1)
	}

	for _, control := range a.controls {
		switch control.hwnd {
		case a.headerTitle, a.headerSub, a.headerMeta, a.log:
			continue
		}
		w := a.scale(control.w)
		if control.w == 0 {
			w = client.right - a.scale(control.x) - a.scale(margin)
		}
		procMoveWindow.Call(control.hwnd,
			uintptr(a.scale(control.x)), uintptr(a.scale(control.y)),
			uintptr(w), uintptr(a.scale(control.h)), 1)
	}

	// "Mostrar token" a la derecha del rótulo del token.
	if check := a.checks["showToken"]; check != 0 {
		procMoveWindow.Call(check,
			uintptr(width-a.scale(margin)-a.scale(120)), uintptr(a.scale(headerH+margin-2)),
			uintptr(a.scale(120)), uintptr(a.scale(18)), 1)
	}

	if a.summary != 0 {
		procMoveWindow.Call(a.summary, uintptr(a.scale(margin)), uintptr(a.scale(summaryY)),
			uintptr(contentW), uintptr(a.scale(20)), 1)
	}

	// Barra de progreso fina sobre los botones.
	procMoveWindow.Call(a.progress, uintptr(a.scale(margin)), uintptr(a.scale(actionsY+14)),
		uintptr(a.scale(240)), uintptr(a.scale(8)), 1)

	right := width - a.scale(margin)
	gap := a.scale(8)
	for _, id := range []int{idBtnBuild, idBtnCheck, idBtnOpenOut} {
		button := a.buttons[id]
		if button == 0 {
			continue
		}
		w := a.scale(buttonWidth(id))
		right -= w
		procMoveWindow.Call(button, uintptr(right), uintptr(a.scale(actionsY)),
			uintptr(w), uintptr(a.scale(36)), 1)
		right -= gap
	}

	if a.log != 0 {
		top := a.scale(logTop + 22)
		bottom := height - a.scale(38)
		if bottom < top+a.scale(80) {
			bottom = top + a.scale(80)
		}
		procMoveWindow.Call(a.log, uintptr(a.scale(margin)), uintptr(top),
			uintptr(contentW), uintptr(bottom-top), 1)
	}
}

func buttonWidth(id int) int32 {
	switch id {
	case idBtnBuild:
		return 200
	case idBtnCheck:
		return 160
	default:
		return 140
	}
}

// --- utilidades ---

func (a *App) setText(hwnd uintptr, value string) {
	if hwnd == 0 {
		return
	}
	procSetWindowText.Call(hwnd, uintptr(unsafe.Pointer(u16(value))))
}

func (a *App) text(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	length, _, _ := procGetWindowTextLength.Call(hwnd)
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, int(length)+1)
	procGetWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), length+1)
	return syscall.UTF16ToString(buffer)
}

func (a *App) field(key string) string {
	return a.text(a.fields[key])
}

func (a *App) setField(key, value string) {
	a.setText(a.fields[key], value)
}

func (a *App) checked(key string) bool {
	hwnd := a.checks[key]
	if hwnd == 0 {
		return false
	}
	state, _, _ := procSendMessage.Call(hwnd, 0x00F0, 0, 0) // BM_GETCHECK
	return state == 1
}

func (a *App) setChecked(key string, value bool) {
	hwnd := a.checks[key]
	if hwnd == 0 {
		return
	}
	flag := uintptr(0)
	if value {
		flag = 1
	}
	procSendMessage.Call(hwnd, 0x00F1, flag, 0) // BM_SETCHECK
}

func (a *App) setStatus(text string) {
	if a.status == 0 {
		return
	}
	procSendMessage.Call(a.status, sbSetText, 0, uintptr(unsafe.Pointer(u16(text))))
}

func (a *App) logLine(format string, args ...any) {
	if a.log == 0 {
		return
	}
	line := fmt.Sprintf(format, args...)
	line = fmt.Sprintf("[%s] %s\r\n", time.Now().Format("15:04:05"), line)

	length, _, _ := procSendMessage.Call(a.log, 0x000E, 0, 0) // WM_GETTEXTLENGTH
	if length > 200000 {
		a.setText(a.log, "")
		length = 0
	}
	procSendMessage.Call(a.log, 0x00B1, length, length) // EM_SETSEL
	procSendMessage.Call(a.log, 0x00C2, 0, uintptr(unsafe.Pointer(u16(line))))
	procSendMessage.Call(a.log, 0x00B7, 0, 0) // EM_SCROLLCARET
}

func (a *App) progressSet(done, total int) {
	if a.progress == 0 {
		return
	}
	if total <= 0 {
		total = 1
	}
	procSendMessage.Call(a.progress, pbmSetPos, uintptr(int32(done)*1000/int32(total)), 0)
}

func (a *App) setBusy(busy bool) {
	a.busy = busy
	for _, id := range []int{idBtnBuild, idBtnCheck, idBtnOpenOut} {
		button := a.buttons[id]
		if button == 0 {
			continue
		}
		enable := uintptr(0)
		if !busy {
			enable = 1
		}
		procEnableWindow.Call(button, enable)
	}
	procShowWindow.Call(a.progress, boolToShow(busy))
	label := "Compilar agente"
	if busy {
		label = "Cancelar"
	}
	a.setText(a.buttons[idBtnBuild], label)
	procEnableWindow.Call(a.buttons[idBtnBuild], 1)
	if busy {
		a.progressSet(0, 100)
	}
}

func boolToShow(value bool) uintptr {
	if value {
		return swShow
	}
	return 0
}

func (a *App) repoOrHint() string {
	if repo := kit.FindRepo(""); repo != "" {
		return repo
	}
	return "(no encuentro go.mod subiendo desde el ejecutable)"
}

func (a *App) onClose() {
	if a.busy {
		if !a.askYes("Hay una compilación en marcha. ¿Cancelar y salir?") {
			return
		}
		if a.cancelFn != nil {
			a.cancelFn()
		}
	}
	a.saveSettings(true)
	procDestroyWindow.Call(a.hwnd)
}

// askYes devuelve true solo si el usuario pulsó Sí. MessageBox contesta
// IDYES (6) en un cuadro Sí/No, no IDOK (1): comparar con 1 hacía que toda
// confirmación se leyera como un no.
func (a *App) askYes(text string) bool {
	result, _, _ := procMessageBox.Call(a.hwnd,
		uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(appTitle))),
		mbYesNo|mbIconWarning)
	return int32(result) == idYes
}

func (a *App) message(kind uint32, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	procMessageBox.Call(a.hwnd, uintptr(unsafe.Pointer(u16(text))),
		uintptr(unsafe.Pointer(u16(appTitle))), uintptr(mbOK|kind))
}
