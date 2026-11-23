//go:build dev

package main

import (
	"log/slog"
	"os"
)

func setupLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
}
