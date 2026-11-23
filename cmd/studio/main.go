//go:build windows

// GoVoid Studio: constructor de agentes con interfaz propia.
//
// El operador pega el token de su bot, el id de su guild y la URL de su
// webhook; la app valida las tres cosas contra Discord, compila una copia del
// agente con esa identidad y deja el binario, la nota de entrega y el .zip en
// la carpeta de salida.
//
// No usa frameworks ni CGO: ventana, controles, menú y diálogos son Win32
// directo, y la compilación es el mismo motor que usa el builder de consola.
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

// singleInstance impide dos copias. Si ya hay una, trae su ventana al frente.
func singleInstance() bool {
	name := u16(`Local\GoVoidStudioSingleInstance`)
	handle, _, callErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return true
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == 183 { // ERROR_ALREADY_EXISTS
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
