//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	ntdll    = windows.NewLazySystemDLL("ntdll.dll")

	procRegisterClassEx        = user32.NewProc("RegisterClassExW")
	procCreateWindowEx         = user32.NewProc("CreateWindowExW")
	procDefWindowProc          = user32.NewProc("DefWindowProcW")
	procGetMessage             = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessage        = user32.NewProc("DispatchMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procSendMessage            = user32.NewProc("SendMessageW")
	procPostMessage            = user32.NewProc("PostMessageW")
	procSetWindowText          = user32.NewProc("SetWindowTextW")
	procGetWindowText          = user32.NewProc("GetWindowTextW")
	procGetWindowTextLength    = user32.NewProc("GetWindowTextLengthW")
	procMoveWindow             = user32.NewProc("MoveWindow")
	procShowWindow             = user32.NewProc("ShowWindow")
	procEnableWindow           = user32.NewProc("EnableWindow")
	procSetFocus               = user32.NewProc("SetFocus")
	procGetClientRect          = user32.NewProc("GetClientRect")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procMessageBox             = user32.NewProc("MessageBoxW")
	procLoadCursor             = user32.NewProc("LoadCursorW")
	procLoadIcon               = user32.NewProc("LoadIconW")
	procCreateMenu             = user32.NewProc("CreateMenu")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenu             = user32.NewProc("AppendMenuW")
	procSetMenu                = user32.NewProc("SetMenu")
	procDrawMenuBar            = user32.NewProc("DrawMenuBar")
	procGetModuleHandle        = kernel32.NewProc("GetModuleHandleW")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procInvalidateRect         = user32.NewProc("InvalidateRect")
	procGetSysColor            = user32.NewProc("GetSysColor")
	procGetSysColorBrush       = user32.NewProc("GetSysColorBrush")
	procAdjustWindowRectEx     = user32.NewProc("AdjustWindowRectEx")
	procSetProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	procGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	procAdjustWindowRectExFor  = user32.NewProc("AdjustWindowRectExForDpi")
	procSetProcessDPIAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procCreateAcceleratorTable = user32.NewProc("CreateAcceleratorTableW")
	procTranslateAccelerator   = user32.NewProc("TranslateAcceleratorW")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procSetCapture             = user32.NewProc("SetCapture")
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procGetKeyState            = user32.NewProc("GetKeyState")
	procSetClassLongPtr        = user32.NewProc("SetClassLongPtrW")
	procSystemParametersInfo   = user32.NewProc("SystemParametersInfoW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procFindWindow             = user32.NewProc("FindWindowW")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procShowWindowAsync        = user32.NewProc("ShowWindowAsync")
	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procEmptyClipboard         = user32.NewProc("EmptyClipboard")
	procSetClipboardData       = user32.NewProc("SetClipboardData")
	procBeginPaint             = user32.NewProc("BeginPaint")
	procEndPaint               = user32.NewProc("EndPaint")
	procFillRect               = user32.NewProc("FillRect")
	procFrameRect              = user32.NewProc("FrameRect")

	procGlobalAlloc   = kernel32.NewProc("GlobalAlloc")
	procGlobalLock    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock  = kernel32.NewProc("GlobalUnlock")
	procGlobalFree    = kernel32.NewProc("GlobalFree")
	procCreateMutex   = kernel32.NewProc("CreateMutexW")
	procRtlMoveMemory = ntdll.NewProc("RtlMoveMemory")
	procGetWindowRect = user32.NewProc("GetWindowRect")

	procInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	procSetWindowTheme       = comctl32.NewProc("SetWindowTheme")

	procCreateFont       = gdi32.NewProc("CreateFontW")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procSetBkColor       = gdi32.NewProc("SetBkColor")
	procSelectObject     = gdi32.NewProc("SelectObject")

	procGetOpenFileName = comdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileName = comdlg32.NewProc("GetSaveFileNameW")

	procSHBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procShellExecute        = shell32.NewProc("ShellExecuteW")
	procCoTaskMemFree       = ole32.NewProc("CoTaskMemFree")
	procCoInitializeEx      = ole32.NewProc("CoInitializeEx")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsTabStop          = 0x00010000
	wsClipChildren     = 0x02000000
	wsClipSiblings     = 0x04000000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000
	wsCaption          = 0x00C00000

	wsExClientEdge = 0x00000200
	wsExAppWindow  = 0x00040000

	esLeft        = 0x0000
	esAutoHScroll = 0x0080
	esPassword    = 0x0020
	esMultiline   = 0x0004
	esReadOnly    = 0x0800
	esWantReturn  = 0x1000
	esNoHideSel   = 0x0100
	esUpper       = 0x0008

	bsPushButton   = 0x00000000
	bsAutoCheckBox = 0x00000003
	bsGroupBox     = 0x00000007

	ssLeft  = 0x00000000
	ssRight = 0x00000002

	cbsDropDown   = 0x0003
	cbsHasStrings = 0x0200

	tcsToolTips = 0x0400

	swShow       = 5
	swShowNormal = 1

	swpNoZOrder = 0x0004
	swpNoMove   = 0x0002
	swpNoSize   = 0x0001

	idiApplication = 32512
	idcArrow       = 32512

	colorWindow     = 5
	colorWindowText = 8
	colorBtnFace    = 15
	colorGrayText   = 17
	colorHighlight  = 13

	wmCreate         = 0x0001
	wmDestroy        = 0x0002
	wmSize           = 0x0005
	wmSetFocus       = 0x0007
	wmKillFocus      = 0x0008
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmQueryEnd       = 0x0011
	wmEraseBkgnd     = 0x0014
	wmGetMinMaxInfo  = 0x0024
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmNotify         = 0x004E
	wmCtlColorEdit   = 0x0133
	wmCtlColorBtn    = 0x0135
	wmCtlColorStatic = 0x0138
	wmDpiChanged     = 0x02E0
	wmApp            = 0x8000
	wmAppRun         = wmApp + 1

	bnClicked = 0
	enChange  = 0x0300

	mbOK              = 0x00000000
	mbIconError       = 0x00000010
	mbIconWarning     = 0x00000030
	mbIconInformation = 0x00000040
	mbYesNo           = 0x00000004

	idOK = 1

	tcmFirst      = 0x1300
	tcmInsertItem = tcmFirst + 7
	tcmGetCurSel  = tcmFirst + 11
	tcmSetCurSel  = tcmFirst + 12
	tcifText      = 0x0001
	tcnSelChange  = -551

	pbmSetPos     = 0x0402
	pbmSetRange32 = 0x0406
	pbmSetState   = 0x0410

	sbSetParts = 0x0404
	sbSetText  = 0x040B

	iccTab      = 0x00000008
	iccBar      = 0x00000004
	iccProgress = 0x00000020

	ofnReadOnly        = 0x00000001
	ofnOverwritePrompt = 0x00000002
	ofnHideReadOnly    = 0x00000004
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnFileMustExist   = 0x00001000
	ofnExplorer        = 0x00080000

	bifReturnOnlyFSDirs = 0x00000001
	bifNewDialogStyle   = 0x00000040
	bifEditBox          = 0x00000010

	fvirtKey = 0x01
	fcontrol = 0x08
	fshift   = 0x04
	falt     = 0x10

	vkF5 = 0x74
	vkF9 = 0x78

	gwlpWndProc = -4
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type rect struct {
	left, top, right, bottom int32
}

type point struct {
	x, y int32
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	private uint32
}

type minMaxInfo struct {
	reserved     point
	maxSize      point
	maxPosition  point
	minTrackSize point
	maxTrackSize point
}

type initCommonControlsEx struct {
	size uint32
	icc  uint32
}

type tcItem struct {
	mask        uint32
	dwState     uint32
	dwStateMask uint32
	pszText     *uint16
	cchTextMax  int32
	iImage      int32
	lParam      uintptr
}

type nmhdr struct {
	hwndFrom uintptr
	idFrom   uintptr
	code     uint32
}

type accel struct {
	fVirt uint8
	key   uint16
	cmd   uint16
}

type openFileName struct {
	StructSize    uint32
	_             uint32
	Owner         uintptr
	Instance      uintptr
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	_             uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	_             uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      uintptr
	Hook          uintptr
	TemplateName  *uint16
	Reserved      uintptr
	Reserved2     uint32
	FlagsEx       uint32
}

type browseInfo struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	Param       uintptr
	Image       int32
}

type paintStruct struct {
	hdc       uintptr
	erase     int32
	paint     rect
	restore   int32
	incUpdate int32
	reserved  [32]byte
}

func u16(value string) *uint16 {
	ptr, err := windows.UTF16PtrFromString(value)
	if err != nil {
		empty := []uint16{0}
		return &empty[0]
	}
	return ptr
}

func loword(value uintptr) int { return int(value & 0xFFFF) }
func hiword(value uintptr) int { return int((value >> 16) & 0xFFFF) }

func sysColor(index int) uintptr {
	value, _, _ := procGetSysColor.Call(uintptr(index))
	return value
}

func sysColorBrush(index int) uintptr {
	value, _, _ := procGetSysColorBrush.Call(uintptr(index))
	return value
}

func dpiAware() {
	if err := procSetProcessDPIAwareness.Find(); err == nil {
		if result, _, _ := procSetProcessDPIAwareness.Call(^uintptr(3)); result != 0 {
			return
		}
	}
	if err := procSetProcessDPIAware.Find(); err == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

func windowDPI(hwnd uintptr) int {
	if err := procGetDpiForWindow.Find(); err == nil && hwnd != 0 {
		if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi >= 96 && dpi <= 480 {
			return int(dpi)
		}
	}
	if dpi, _, _ := user32.NewProc("GetDpiForSystem").Call(); dpi >= 96 && dpi <= 480 {
		return int(dpi)
	}
	return 96
}
