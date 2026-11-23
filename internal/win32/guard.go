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

// RunGuard es el cuerpo de un centinela. Vive en su propio proceso, toma el
// mutex de su slot y mantiene una cortina completa —ventana, BlockInput y
// hooks— mientras la bandera de bloqueo exista.
//
// Un centinela caído no libera el candado: los otros tres siguen con el suyo.
// Y el que sobrevive vuelve a levantar al que falta en el siguiente tick.
func RunGuard(slot int) {
	if slot < 0 || slot >= guardSlots {
		return
	}

	// El mutex pertenece a un hilo. Se queda tomado hasta que el proceso muere,
	// y ahí Windows lo marca abandonado para que el siguiente lo recupere.
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
		return // el slot ya lo tiene otro centinela
	}

	if !LockRequested() {
		return
	}

	lock := &LockScreen{}
	if err := lock.Start(0); err != nil {
		// Sin cortina no hay centinela útil. Se espera antes de salir para que
		// el que repone no entre en un bucle de arranque cada 500 ms.
		time.Sleep(10 * time.Second)
		return
	}
	defer lock.Stop()
	defer func() {
		// Si este centinela sale porque la bandera ya no está, el hardware de
		// entrada tiene que volver aunque el agente no esté para hacerlo.
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

// ensurePeers repone los centinelas que falten. El mutex es la liveness: si
// OpenMutex no encuentra el objeto, el dueño murió.
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

// ensureAgent comprueba que el agente principal sigue vivo. Sin él no hay
// /lockscreen action:off, así que un centinela lo relanza.
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

// GuardSlot convierte el argumento de línea de comandos en un slot.
// Cualquier cosa que no sea un entero válido devuelve -1.
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
