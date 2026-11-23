//go:build windows

package win32

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Ctrl+Alt+Del no se puede interceptar: es la Secure Attention Sequence, la
// genera el teclado y la atiende el kernel en un escritorio aparte. Ningún
// hook de userland la ve, y BlockInput no la toca.
//
// La única forma de que no exista es que el teclado no exista. Deshabilitar
// la clase Keyboard en el árbol PnP descarga el driver del dispositivo: el
// kernel no recibe ni un scancode, así que no hay SAS, no hay Win+L, no hay
// nada. Es la misma llamada que hace el Administrador de dispositivos.
//
// Se hace lo mismo con la clase Mouse. BlockInput(FALSE) lo puede llamar
// cualquier proceso; un dispositivo deshabilitado, no.
var (
	guidDevClassKeyboard = guid{0x4D36E96B, 0xE325, 0x11CE,
		[8]byte{0xBF, 0xC1, 0x08, 0x00, 0x2B, 0xE1, 0x03, 0x18}}
	guidDevClassMouse = guid{0x4D36E96F, 0xE325, 0x11CE,
		[8]byte{0xBF, 0xC1, 0x08, 0x00, 0x2B, 0xE1, 0x03, 0x18}}
)

const (
	digcfPresent = 0x00000002

	difPropertyChange = 0x00000012

	dicsEnable  = 0x00000001
	dicsDisable = 0x00000002
	dicsGlobal  = 0x00000001
)

// inputDisabled recuerda si esta ejecución apagó los dispositivos. Sirve para
// no recorrer el árbol PnP cada dos segundos cuando no hay nada que reponer.
var inputDisabled atomic.Bool

// inputMarkerPath deja constancia en disco de que el hardware se apagó. Sin
// esta marca, el arranque del agente no toca los dispositivos: habilitar un
// teclado que ya estaba habilitado lo reenumera y provoca un parpadeo que no
// hace falta en una máquina sana.
func inputMarkerPath() string {
	return filepath.Join(guardDir(), "input.off")
}

func markInputDisabled() {
	_ = os.MkdirAll(guardDir(), 0o755)
	_ = os.WriteFile(inputMarkerPath(), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644)
}

func clearInputMarker() {
	_ = os.Remove(inputMarkerPath())
}

func inputMarkerPresent() bool {
	_, err := os.Stat(inputMarkerPath())
	return err == nil
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type spDevInfoData struct {
	Size      uint32
	ClassGUID guid
	DevInst   uint32
	Reserved  uintptr
}

type spClassInstallHeader struct {
	Size            uint32
	InstallFunction uint32
}

type spPropChangeParams struct {
	ClassInstallHeader spClassInstallHeader
	StateChange        uint32
	Scope              uint32
	HwProfile          uint32
}

var (
	setupapi = windows.NewLazySystemDLL("setupapi.dll")

	procSetupDiGetClassDevsW          = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInfo         = setupapi.NewProc("SetupDiEnumDeviceInfo")
	procSetupDiSetClassInstallParamsW = setupapi.NewProc("SetupDiSetClassInstallParamsW")
	procSetupDiCallClassInstaller     = setupapi.NewProc("SetupDiCallClassInstaller")
	procSetupDiDestroyDeviceInfoList  = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

// disableInputDevices apaga teclados y ratones a nivel de dispositivo.
// Idempotente: sobre un dispositivo ya deshabilitado la llamada falla sin
// efecto.
func disableInputDevices() {
	// La marca va primero: si el proceso muere a mitad del apagado, el
	// siguiente arranque sabe que hay que devolver el hardware.
	markInputDisabled()

	keyboards := setDeviceState(&guidDevClassKeyboard, dicsDisable)
	mice := setDeviceState(&guidDevClassMouse, dicsDisable)
	if keyboards+mice > 0 {
		inputDisabled.Store(true)
	}
}

// enableInputDevices devuelve el teclado y el ratón. Si nunca se apagaron no
// hace nada: la marca en disco es la que decide.
func enableInputDevices() {
	if !inputMarkerPresent() && !inputDisabled.Load() {
		return
	}

	setDeviceState(&guidDevClassKeyboard, dicsEnable)
	setDeviceState(&guidDevClassMouse, dicsEnable)

	inputDisabled.Store(false)
	clearInputMarker()
}

// InputDevicesDisabled informa si esta ejecución apagó el hardware de entrada.
func InputDevicesDisabled() bool {
	return inputDisabled.Load()
}

// countDevices cuenta los dispositivos de una clase sin tocar su estado. Es
// solo lectura: sirve para comprobar que el GUID y las estructuras son
// correctos sin deshabilitar el teclado de la máquina que ejecuta la prueba.
func countDevices(class *guid) int {
	devices, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(class)), 0, 0, digcfPresent)
	if devices == 0 || devices == ^uintptr(0) {
		return 0
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devices)

	count := 0
	for index := uintptr(0); ; index++ {
		var info spDevInfoData
		info.Size = uint32(unsafe.Sizeof(info))
		ok, _, _ := procSetupDiEnumDeviceInfo.Call(
			devices, index, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			break
		}
		count++
	}
	return count
}

// RestoreInputDevices devuelve teclado y ratón si un cierre a medias los dejó
// apagados. Lo llama el agente al arrancar sin bandera.
func RestoreInputDevices() {
	enableInputDevices()
}

// setDeviceState recorre todos los dispositivos de una clase y les aplica el
// cambio de estado. Devuelve cuántos cambiaron de verdad.
//
// Se prueban dos conjuntos de flags: primero solo los presentes, y si no hay
// ninguno, todos los de la clase. Un dispositivo deshabilitado puede dejar de
// contar como presente, y entonces la habilitación no lo encontraría.
func setDeviceState(class *guid, state uint32) int {
	changed := applyDeviceState(class, state, digcfPresent)
	if changed > 0 {
		return changed
	}
	return applyDeviceState(class, state, 0)
}

func applyDeviceState(class *guid, state uint32, flags uintptr) int {
	devices, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(class)), 0, 0, flags)
	if devices == 0 || devices == ^uintptr(0) {
		return 0
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devices)

	changed := 0

	for index := uintptr(0); ; index++ {
		var info spDevInfoData
		info.Size = uint32(unsafe.Sizeof(info))

		ok, _, _ := procSetupDiEnumDeviceInfo.Call(
			devices, index, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			break // ERROR_NO_MORE_ITEMS
		}

		var params spPropChangeParams
		params.ClassInstallHeader.Size = uint32(unsafe.Sizeof(params.ClassInstallHeader))
		params.ClassInstallHeader.InstallFunction = difPropertyChange
		params.StateChange = state
		params.Scope = dicsGlobal

		set, _, _ := procSetupDiSetClassInstallParamsW.Call(
			devices,
			uintptr(unsafe.Pointer(&info)),
			uintptr(unsafe.Pointer(&params)),
			unsafe.Sizeof(params))
		if set == 0 {
			continue
		}

		applied, _, _ := procSetupDiCallClassInstaller.Call(
			difPropertyChange, devices, uintptr(unsafe.Pointer(&info)))
		if applied != 0 {
			changed++
		}
	}

	return changed
}
