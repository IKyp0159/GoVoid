//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/local/govoid/internal/config"
)

const (
	guardSlots = 4

	lockFlagName  = "locked.flag"
	agentPathName = "agent.path"
	guardExeFmt   = "g%d.exe"
)

var lockFlagOverride string

func lockFlagPath() string {
	if lockFlagOverride != "" {
		return lockFlagOverride
	}
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, config.DataDirName, lockFlagName)
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, config.DataDirName, lockFlagName)
}

func guardDir() string {
	return filepath.Dir(lockFlagPath())
}

func LockRequested() bool {
	_, err := os.Stat(lockFlagPath())
	return err == nil
}

func RequestLock() error {
	path := lockFlagPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("lockscreen: no se pudo crear %s: %w", filepath.Dir(path), err)
	}
	stamp := []byte(time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(path, stamp, 0o644); err != nil {
		return fmt.Errorf("lockscreen: no se pudo escribir la bandera: %w", err)
	}
	return nil
}

func RequestUnlock() error {
	err := os.Remove(lockFlagPath())
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func guardMutexName(slot int) string {
	return fmt.Sprintf(config.GuardMutexFmt, slot)
}

func guardExePath(slot int) string {
	return filepath.Join(guardDir(), fmt.Sprintf(guardExeFmt, slot))
}

func agentPathFile() string {
	return filepath.Join(guardDir(), agentPathName)
}

func writeAgentPath(exe string) error {
	if err := os.MkdirAll(guardDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(agentPathFile(), []byte(exe), 0o644)
}

func readAgentPath() string {
	payload, err := os.ReadFile(agentPathFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(payload))
}

func guardAlive(slot int) bool {
	name, err := windows.UTF16PtrFromString(guardMutexName(slot))
	if err != nil {
		return false
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(handle)
	return true
}

func ensureGuardCopy(exe string, slot int) (string, error) {
	target := guardExePath(slot)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	sourceInfo, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	if targetInfo, statErr := os.Stat(target); statErr == nil {
		if targetInfo.Size() == sourceInfo.Size() &&
			targetInfo.ModTime().Equal(sourceInfo.ModTime()) {
			return target, nil
		}
	}

	payload, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	tmp := target + ".new"
	if err := os.WriteFile(tmp, payload, 0o755); err != nil {
		return "", err
	}
	_ = os.Chtimes(tmp, sourceInfo.ModTime(), sourceInfo.ModTime())
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return target, nil
}

func SpawnGuards(exe string) {
	for slot := 0; slot < guardSlots; slot++ {
		if guardAlive(slot) {
			continue
		}
		path, err := ensureGuardCopy(exe, slot)
		if err != nil {
			continue
		}
		spawnGuard(path, slot)
	}
}

func spawnGuard(path string, slot int) {
	cmd := exec.Command(path, "--lockguard", strconv.Itoa(slot))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindowFlag,
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

const createNoWindowFlag = 0x08000000

func guardsAlive() int {
	alive := 0
	for slot := 0; slot < guardSlots; slot++ {
		if guardAlive(slot) {
			alive++
		}
	}
	return alive
}

func GuardSlots() int { return guardSlots }

func GuardCount() int { return guardsAlive() }

func removeGuardCopies() error {
	var errs []error
	for slot := 0; slot < guardSlots; slot++ {
		path := guardExePath(slot)
		deadline := time.Now().Add(4 * time.Second)
		for {
			err := os.Remove(path)
			if err == nil || errors.Is(err, os.ErrNotExist) {
				break
			}
			if time.Now().After(deadline) {
				errs = append(errs, fmt.Errorf("centinela %d: %w", slot, err))
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return errors.Join(errs...)
}
