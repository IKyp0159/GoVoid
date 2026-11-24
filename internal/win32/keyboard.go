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

var inputDisabled atomic.Bool

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

func disableInputDevices() {
	markInputDisabled()

	keyboards := setDeviceState(&guidDevClassKeyboard, dicsDisable)
	mice := setDeviceState(&guidDevClassMouse, dicsDisable)
	if keyboards+mice > 0 {
		inputDisabled.Store(true)
	}
}

func enableInputDevices() {
	if !inputMarkerPresent() && !inputDisabled.Load() {
		return
	}

	setDeviceState(&guidDevClassKeyboard, dicsEnable)
	setDeviceState(&guidDevClassMouse, dicsEnable)

	inputDisabled.Store(false)
	clearInputMarker()
}

func InputDevicesDisabled() bool {
	return inputDisabled.Load()
}

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

func RestoreInputDevices() {
	enableInputDevices()
}

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
			break
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
