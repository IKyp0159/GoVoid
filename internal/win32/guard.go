//go:build windows

package win32

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/local/govoid/internal/config"
)

func RunGuard(slot int) {
	if slot < 0 || slot >= guardSlots {
		return
	}

	runtime.LockOSThread()

	name, err := windows.UTF16PtrFromString(guardMutexName(slot))
	if err != nil {
		return
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	wait, err := windows.WaitForSingleObject(handle, 0)
	if err != nil && wait != windows.WAIT_ABANDONED {
		return
	}
	if wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED {
		return
	}

	if !LockRequested() {
		return
	}

	lock := &LockScreen{}
	if err := lock.Start(0); err != nil {
		time.Sleep(10 * time.Second)
		return
	}
	defer lock.Stop()
	defer func() {
		if !LockRequested() {
			enableInputDevices()
		}
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		if !LockRequested() {
			return
		}
		ensurePeers(slot)
		ensureAgent()
	}
}

func ensurePeers(mySlot int) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	for slot := 0; slot < guardSlots; slot++ {
		if slot == mySlot || guardAlive(slot) {
			continue
		}
		path, err := ensureGuardCopy(self, slot)
		if err != nil {
			continue
		}
		spawnGuard(path, slot)
	}
}

func ensureAgent() {
	name, err := windows.UTF16PtrFromString(config.MutexName)
	if err != nil {
		return
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err == nil {
		_ = windows.CloseHandle(handle)
		return
	}

	agent := readAgentPath()
	if agent == "" {
		return
	}
	if _, err := os.Stat(agent); err != nil {
		return
	}

	cmd := exec.Command(agent)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindowFlag,
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

func GuardSlot(args []string) int {
	if len(args) < 1 {
		return -1
	}
	slot, err := strconv.Atoi(args[0])
	if err != nil || slot < 0 || slot >= guardSlots {
		return -1
	}
	return slot
}
