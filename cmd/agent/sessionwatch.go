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

func runSessionWatch() {
	say := sessionWatchLogger()
	defer say.close()

	session, err := win32.ActiveConsoleSession()
	if err != nil {
		say.logf("sin sesión de consola: %v", err)
		return
	}

	if running := win32.RunningInSession(config.BinaryName, session); running > 0 {
		if win32.HeartbeatFresh(5 * time.Minute) {
			return
		}
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
		say.logf("sesión %d: no se pudo arrancar: %v", session, err)
		return
	}
	say.logf("sesión %d: agente arrancado pid=%d desde %s", session, pid, exe)
}

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
