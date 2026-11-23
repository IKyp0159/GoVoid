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

// IsAdmin informa si el proceso tiene el token elevado. No basta con ser del
// grupo Administradores: con UAC el token viaja filtrado y las escrituras en
// HKLM y System32 fallan igual.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// elevationMarkerPath es el aviso que deja el hijo elevado para que el padre
// sepa que el UAC se aceptó y que puede retirarse.
func elevationMarkerPath() string {
	return filepath.Join(guardDir(), elevationMarkerName)
}

// WriteElevationMarker lo llama el hijo elevado nada más arrancar. Se escribe
// antes de pedir el mutex a propósito: el padre necesita la señal para soltar
// el suyo, y el hijo no puede tomarlo mientras el padre viva.
func WriteElevationMarker() error {
	path := elevationMarkerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// ClearElevationMarker borra el aviso de una elevación anterior.
func ClearElevationMarker() {
	_ = os.Remove(elevationMarkerPath())
}

// WaitElevationMarker espera a que aparezca el pid de un hijo elevado vivo.
// Devuelve falso si el plazo vence: significa que el UAC se canceló o que el
// hijo murió antes de arrancar, y el padre debe seguir vivo.
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

// RequestElevation relanza este ejecutable con el verbo runas. Windows muestra
// el UAC en la sesión interactiva: si el operador remoto no puede pulsar Sí, la
// elevación no ocurre y el agente actual sigue como estaba.
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

	// ShellExecuteW devuelve un código <= 32 cuando falla; 5 es UAC cancelado
	// o denegado, 31 no hay asociación para el verbo.
	if result <= shellSuccessMin {
		if callErr != nil && callErr.Error() != "The operation completed successfully." {
			return fmt.Errorf("elevación: ShellExecuteW devolvió %d (%v)", result, callErr)
		}
		return fmt.Errorf("elevación: ShellExecuteW devolvió %d (UAC cancelado o denegado)", result)
	}
	return nil
}

// processAlive comprueba que un pid sigue existiendo sin pedir más derechos que
// los de sincronización.
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

	const stillActive = 0x00000103 // STATUS_PENDING no aplica a procesos
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return true // existe pero no se puede esperar: cuenta como vivo
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}
