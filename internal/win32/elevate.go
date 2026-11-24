//go:build windows

package win32

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

const (
	swShowNormal        = 1
	shellSuccessMin     = 32
	elevationMarkerName = "elevated.pid"
)

func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func elevationMarkerPath() string {
	return filepath.Join(guardDir(), elevationMarkerName)
}

func WriteElevationMarker() error {
	path := elevationMarkerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func ClearElevationMarker() {
	_ = os.Remove(elevationMarkerPath())
}

func WaitElevationMarker(timeout time.Duration) (int, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(elevationMarkerPath())
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(payload)))
			if convErr == nil && pid > 0 && pid != os.Getpid() && processAlive(uint32(pid)) {
				return pid, true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0, false
}

func RequestElevation() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("elevación: no se pudo resolver el ejecutable: %w", err)
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString("--elevated")
	if err != nil {
		return err
	}

	result, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		swShowNormal)

	if result <= shellSuccessMin {
		if callErr != nil && callErr.Error() != "The operation completed successfully." {
			return fmt.Errorf("elevación: ShellExecuteW devolvió %d (%v)", result, callErr)
		}
		return fmt.Errorf("elevación: ShellExecuteW devolvió %d (UAC cancelado o denegado)", result)
	}
	return nil
}

func processAlive(pid uint32) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer func() {
		if cerr := windows.CloseHandle(handle); cerr != nil {
			_ = cerr
		}
	}()

	const stillActive = 0x00000103
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return true
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}
