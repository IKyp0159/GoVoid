//go:build !dev

package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/local/govoid/internal/config"
)

// logPath es el diario del agente en release. Sin consola, un panic o un
// fallo del bugcheck desaparecía sin rastro; con esto queda escrito.
func logPath() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, config.DataDirName, "agent.log")
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, config.DataDirName, "agent.log")
}

func setupLogging() {
	path := logPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return
	}
	// Un solo archivo, tope de 2 MB: el agente vive meses en la misma máquina.
	if info, err := os.Stat(path); err == nil && info.Size() > 2<<20 {
		_ = os.Rename(path, path+".old")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug})))
}
