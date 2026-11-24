//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func main() {
	runtime.LockOSThread()

	if !singleInstance() {
		return
	}

	smokeReport := os.Getenv("GOVOID_SMOKE_OUT")
	smoke = smokeReport != ""

	if err := runApp(); err != nil {
		procMessageBox.Call(0,
			uintptr(unsafe.Pointer(u16("No se pudo abrir GoVoid Studio:\n\n"+err.Error()))),
			uintptr(unsafe.Pointer(u16(appTitle))), mbOK|mbIconError)
		os.Exit(1)
	}
}

var smoke bool

func singleInstance() bool {
	name := u16(`Local\GoVoidStudioSingleInstance`)
	handle, _, callErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return true
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == 183 {
		existing, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(u16("GoVoidStudioWindow"))), 0)
		if existing != 0 {
			procShowWindowAsync.Call(existing, swShowNormal)
			procSetForegroundWindow.Call(existing)
		}
		return false
	}
	return true
}

func writeSmokeReport(path string) {
	if path == "" || app == nil {
		return
	}
	report := fmt.Sprintf("ventana=%d\ncontroles=%d\ncampos=%d\nchecks=%d\nbotones=%d\nlog=%d\ndpi=%d\nrepo=%s\nconfig=%s\nresumen=%s\n",
		app.hwnd, len(app.controls), len(app.fields), len(app.checks), len(app.buttons), app.log, app.dpi,
		app.field("repo"), defaultSettingsPath(), app.text(app.summary))
	_ = os.WriteFile(path, []byte(report), 0o644)
}
