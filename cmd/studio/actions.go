//go:build windows

package main

// Acciones de la ventana: probar la conexión, compilar y las utilidades del
// menú. La identidad del cliente no se pide: se deriva de la guild y del bot.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/govoid/internal/kit"
)

func (a *App) onCommand(id, code int, lparam uintptr) {
	if code == enChange && id == 0 && lparam == a.fields["guild"] {
		a.updateSummary()
		return
	}

	switch id {
	case idFileOpen:
		a.openSettings()
	case idFileSave:
		a.saveSettings(false)
	case idFileExit:
		a.onClose()

	case idToolsCheck, idBtnCheck:
		a.startCheck()
	case idToolsBuild, idBtnBuild:
		if a.busy {
			a.cancelWork()
		} else {
			a.startBuild()
		}
	case idToolsOpenOut, idBtnOpenOut:
		a.openOutput()
	case idToolsInvite:
		a.copyInvite()
	case idToolsPortal:
		openURL("https://discord.com/developers/applications")
	case idToolsCopyLog:
		copyText(a.text(a.log))
		a.setStatus("Actividad copiada al portapapeles")
	case idToolsSaveLog:
		a.saveLog()
	case idChkShowToken:
		a.applyTokenMask(a.checked("showToken"))
	case idHelpAbout:
		a.showAbout()
	}
}

// --- derivados ---

// autoID construye el identificador del cliente a partir de la guild. Tiene que
// empezar por letra y ser alfanumérico porque acaba en nombres de objeto del
// kernel (mutex, servicio, tareas).
func autoID(guild string) string {
	digits := make([]rune, 0, 8)
	for _, char := range guild {
		if char >= '0' && char <= '9' {
			digits = append(digits, char)
		}
	}
	if len(digits) == 0 {
		return "GVCliente"
	}
	if len(digits) > 6 {
		digits = digits[len(digits)-6:]
	}
	return "GV" + string(digits)
}

// optionsForBuild arma la identidad completa. El nombre sale del bot cuando la
// validación lo conoce; si no, se usa uno neutro.
func (a *App) optionsForBuild(botName string) kit.Options {
	guild := strings.TrimSpace(a.field("guild"))
	id := autoID(guild)
	name := strings.TrimSpace(botName)
	if name == "" {
		name = "GoVoid"
	}

	repo := kit.FindRepo("")
	outDir := ""
	if repo != "" {
		outDir = filepath.Join(repo, "dist", id)
	}

	return kit.Options{
		ID:             id,
		Name:           name,
		Token:          strings.TrimSpace(a.field("token")),
		Guild:          guild,
		Webhook:        strings.TrimSpace(a.field("webhook")),
		ChannelPrefix:  "sesion-",
		BinaryName:     strings.ToLower(id) + ".exe",
		RunKey:         id + "Update",
		TaskLogon:      id + "Update",
		TaskBoot:       id + "UpdateBoot",
		ServiceDisplay: name + " Shield",
		Repo:           repo,
		OutDir:         outDir,
		Package:        true,
		ForceShared:    a.forceShared,
	}
}

func (a *App) updateSummary() {
	guild := strings.TrimSpace(a.field("guild"))
	if guild == "" {
		a.setText(a.summary, "El identificador y el ejecutable se calculan solos al escribir la guild.")
		return
	}
	id := autoID(guild)
	repo := kit.FindRepo("")
	out := filepath.Join("dist", id)
	if repo != "" {
		out = filepath.Join(repo, "dist", id)
	}
	a.setText(a.summary, fmt.Sprintf("Se generará  %s  ·  %s  ·  %s", id, strings.ToLower(id)+".exe", out))
}

// --- trabajos ---

func (a *App) startCheck() {
	if a.busy {
		return
	}
	opt := a.optionsForBuild("")
	if err := opt.ValidateRemote(); err != nil {
		a.fail(err)
		return
	}

	a.saveSettings(true)
	a.setBusy(true)
	a.logLine("")
	a.logLine("probando conexión...")
	a.setStatus("Validando token, guild y webhook...")

	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFn = cancel
	started := time.Now()

	logf := kit.Logf(func(line string) {
		a.post(func() { a.logLine("%s", line) })
	})

	go func() {
		defer cancel()
		report, err := opt.Check(ctx, logf)
		a.post(func() { a.finishCheck(report, err, time.Since(started)) })
	}()
}

func (a *App) startBuild() {
	if a.busy {
		return
	}
	opt := a.optionsForBuild("")
	if err := opt.ValidateRemote(); err != nil {
		a.fail(err)
		return
	}
	if kit.FindRepo("") == "" {
		a.fail(fmt.Errorf("no encuentro la raíz del proyecto: deja GoVoidStudio.exe dentro del repo o en una subcarpeta"))
		return
	}

	// El mismo token del agente que ya corre aquí es un choque real: dos
	// sesiones de gateway se expulsan. Se avisa y decide el usuario, porque
	// recompilar su propio cliente es un caso legítimo.
	if _, err := opt.GuardSharedToken(); err != nil {
		if !a.askYes(err.Error() + "\n\n¿Compilar de todos modos?") {
			a.setStatus("Compilación cancelada")
			return
		}
		a.forceShared = true
	}

	a.saveSettings(true)
	a.setBusy(true)
	a.canceled = false
	a.progressSet(0, 100)
	a.logLine("")
	a.logLine("compilando %s...", autoID(strings.TrimSpace(a.field("guild"))))
	a.setStatus("Validando credenciales...")

	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFn = cancel
	started := time.Now()

	lines := 0
	logf := kit.Logf(func(line string) {
		a.post(func() {
			a.logLine("%s", line)
			lines++
			if position := 30 + lines*3; position < 95 {
				a.progressSet(position, 100)
			}
		})
	})

	go func() {
		defer cancel()

		report, err := opt.Check(ctx, logf)
		if err == nil {
			a.post(func() { a.progressSet(30, 100) })
			build := a.optionsForBuild(report.BotName)
			warning, guardErr := build.GuardSharedToken()
			if guardErr != nil {
				err = guardErr
			} else {
				if warning != "" {
					logf(fmt.Sprintf("  aviso      : %s", warning))
				}
				var result *kit.Result
				result, err = kit.Build(ctx, build, logf)
				if err == nil {
					a.post(func() {
						a.progressSet(100, 100)
						a.finishBuild(build, result, nil, time.Since(started))
					})
					return
				}
			}
		}
		a.post(func() { a.finishBuild(kit.Options{}, nil, err, time.Since(started)) })
	}()
}

func (a *App) cancelWork() {
	if a.cancelFn == nil {
		return
	}
	a.canceled = true
	a.cancelFn()
	a.logLine("cancelando...")
	a.setStatus("Cancelando")
}

func (a *App) finishCheck(report kit.Report, err error, elapsed time.Duration) {
	a.setBusy(false)
	a.cancelFn = nil
	if err != nil {
		a.fail(err)
		return
	}
	a.logLine("conexión correcta con %s (%s) en %s", report.BotName, report.BotID, elapsed.Round(time.Millisecond))
	if report.Warning != "" {
		a.logLine("aviso: %s", report.Warning)
	}
	a.setStatus(fmt.Sprintf("Conexión correcta: %s · guild %s", report.BotName, report.GuildName))
	a.updateSummary()
	a.message(mbIconInformation,
		"Conexión correcta.\n\nBot: %s\nGuild: %s\nWebhook: canal %s\n\nEl cliente se llamará %s y el ejecutable %s.exe.",
		report.BotName, report.GuildName, report.WebhookChannel,
		report.BotName, strings.ToLower(autoID(strings.TrimSpace(a.field("guild")))))
}

func (a *App) finishBuild(opt kit.Options, result *kit.Result, err error, elapsed time.Duration) {
	a.setBusy(false)
	a.cancelFn = nil
	a.forceShared = false

	if err != nil {
		if a.canceled {
			a.logLine("cancelado")
			a.setStatus("Cancelado")
			return
		}
		a.fail(err)
		return
	}

	a.lastOut = result.OutDir
	a.logLine("")
	a.logLine("listo: %s · %.1f s", result.Name, elapsed.Seconds())
	a.logLine("sha256: %s", result.SHA256)
	if result.ZipPath != "" {
		a.logLine("paquete: %s", result.ZipPath)
	}
	a.setStatus(fmt.Sprintf("Compilado %s en %.1f s", result.Name, elapsed.Seconds()))

	extra := ""
	if result.ZipPath != "" {
		extra = "\nPaquete: " + filepath.Base(result.ZipPath)
	}
	if a.askYes(fmt.Sprintf("Cliente %s listo en %.1f s.\n\n%s%s\nSHA-256: %s\n\n¿Abrir la carpeta de salida?",
		opt.ID, elapsed.Seconds(), result.Path, extra, result.SHA256)) {
		a.openOutput()
	}
}

// --- utilidades ---

func (a *App) fail(err error) {
	a.logLine("ERROR: %v", err)
	a.setStatus("Error: " + firstLine(err.Error()))
	a.message(mbIconError, "%v", err)
}

func (a *App) copyInvite() {
	link, err := kit.InviteURL(strings.TrimSpace(a.field("token")))
	if err != nil {
		a.fail(err)
		return
	}
	copyText(link)
	a.logLine("invitación copiada: %s", link)
	a.setStatus("Invitación copiada al portapapeles")
	a.message(mbIconInformation,
		"Invitación copiada al portapapeles.\n\n%s\n\nPega el enlace, elige el servidor y autoriza.\n"+
			"Permisos: gestionar canales, enviar mensajes, incrustar enlaces, adjuntar archivos,\n"+
			"gestionar mensajes y leer el historial.", link)
}

func (a *App) openOutput() {
	out := a.lastOut
	if out == "" {
		guild := strings.TrimSpace(a.field("guild"))
		repo := kit.FindRepo("")
		if repo != "" && guild != "" {
			out = filepath.Join(repo, "dist", autoID(guild))
		}
	}
	if out == "" {
		a.message(mbIconWarning, "Todavía no hay carpeta de salida.\n\nCompila un agente primero.")
		return
	}
	if _, err := os.Stat(out); err != nil {
		a.message(mbIconWarning, "La carpeta de salida no existe todavía:\n%s", out)
		return
	}
	openFolder(out)
}

func (a *App) saveLog() {
	text := a.text(a.log)
	if text == "" {
		a.message(mbIconWarning, "La actividad está vacía.")
		return
	}
	path := a.askSaveFile("Guardar actividad", "Texto (*.txt)\x00*.txt\x00Todos los archivos (*.*)\x00*.*\x00", "govoid-studio.log")
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		a.message(mbIconError, "No se pudo escribir %s:\n%v", path, err)
		return
	}
	a.setStatus("Actividad guardada en " + filepath.Base(path))
}

func (a *App) showAbout() {
	a.message(mbIconInformation,
		"%s %s\n\nUn bot, un servidor, un ejecutable.\n\n"+
			"Pega el token, el id de la guild y el webhook: la app valida las tres cosas,\n"+
			"deriva el identificador, compila el agente con esa identidad y deja el\n"+
			"binario, la nota de entrega y el .zip en dist\\.\n\n"+
			"Go 1.22+, sin CGO. Configuración: %s",
		appTitle, appVersion, defaultSettingsPath())
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return strings.TrimSpace(text)
}
