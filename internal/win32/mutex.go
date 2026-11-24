//go:build windows

package win32

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

var instanceMutex windows.Handle

var ErrAlreadyRunning = errors.New("win32: ya hay una instancia del agente corriendo")

func AcquireSingleInstance(name string, wait time.Duration) error {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("win32: nombre de mutex inválido: %w", err)
	}

	deadline := time.Now().Add(wait)
	for {
		handle, err := windows.CreateMutex(nil, false, namePtr)
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if handle != 0 {
				if cerr := windows.CloseHandle(handle); cerr != nil {
					_ = cerr
				}
			}
			if time.Now().After(deadline) {
				return ErrAlreadyRunning
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if err != nil {
			return fmt.Errorf("win32: CreateMutex: %w", err)
		}
		if handle == 0 {
			return errors.New("win32: CreateMutex devolvió handle nulo")
		}

		instanceMutex = handle
		return nil
	}
}

func ReleaseSingleInstance() {
	if instanceMutex == 0 {
		return
	}
	if err := windows.CloseHandle(instanceMutex); err != nil {
		_ = err
	}
	instanceMutex = 0
}
