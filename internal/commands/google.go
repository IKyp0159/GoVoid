//go:build windows

package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/ui"
	"github.com/local/govoid/internal/util"
	"github.com/local/govoid/internal/win32"
)

func Google(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	googleResult(s, i, win32.CloseGoogleSessions(nil))
}

func GooglePanel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	googlePanel(s, i, -1)
}

func GoogleRefresh(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	googlePanel(s, i, -1)
}

func GoogleSelect(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	values := i.MessageComponentData().Values
	selected := -1
	if len(values) > 0 {
		if parsed, err := strconv.Atoi(values[0]); err == nil {
			selected = parsed
		}
	}
	googlePanel(s, i, selected)
}

func GoogleCloseOne(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	index := googleIndex(i.MessageComponentData().CustomID)
	profiles := win32.GoogleProfiles()
	if index < 0 || index >= len(profiles) {
		googlePanel(s, i, -1)
		return
	}
	googleResult(s, i, win32.CloseGoogleSessions([]string{profiles[index].Name}))
}

func GoogleCloseAll(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	googleResult(s, i, win32.CloseGoogleSessions(nil))
}

func googleIndex(customID string) int {
	const prefix = "btn_google_one_"
	if !strings.HasPrefix(customID, prefix) {
		return -1
	}
	index, err := strconv.Atoi(strings.TrimPrefix(customID, prefix))
	if err != nil {
		return -1
	}
	return index
}

func googlePanel(s *discordgo.Session, i *discordgo.InteractionCreate, selected int) {
	profiles := win32.GoogleProfiles()
	content, components := googlePanelView(profiles, selected)
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &components,
	})
}

func googleResult(s *discordgo.Session, i *discordgo.InteractionCreate, report win32.GoogleCloseReport) {
	content := googleResultView(report)
	components := ui.GoogleResultPanel()
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &components,
	})
}

func googlePanelView(profiles []win32.GoogleProfile, selected int) (string, []discordgo.MessageComponent) {
	options := make([]discordgo.SelectMenuOption, 0, len(profiles))
	for index, profile := range profiles {
		description := fmt.Sprintf("%s · %d cookies Google", win32.FormatGoogleAccount(profile.Account), profile.GoogleCookies)
		if profile.Session {
			description += " · sesión activa"
		}
		options = append(options, discordgo.SelectMenuOption{
			Label:       profile.Name,
			Value:       strconv.Itoa(index),
			Description: util.SafeCut(description, 100),
		})
	}

	var out strings.Builder
	out.WriteString("🚪 **Cierre de sesiones de Google**\n\n")

	if len(profiles) == 0 {
		out.WriteString("No se encontró ningún perfil de navegador Chromium con datos.\n")
		out.WriteString("Abre Chrome con la cuenta de Google y pulsa **Actualizar**.\n")
		return util.SafeCut(out.String(), 1900), ui.GooglePanel(-1, options)
	}

	sessions, running := win32.GoogleSummary(profiles)
	fmt.Fprintf(&out, "Perfiles: **%d** · Sesiones activas: **%d** · Navegadores abiertos: **%d**\n\n", len(profiles), sessions, running)

	for index, profile := range profiles {
		marker := "▫️"
		if index == selected {
			marker = "👉"
		}
		state := "sin sesión"
		if profile.Session {
			state = "sesión activa"
		}
		open := "cerrado"
		if profile.Running {
			open = "abierto"
		}
		fmt.Fprintf(&out, "%s `%d` **%s** — %s — %d cookies Google — %s — %s\n",
			marker, index, profile.Name, win32.FormatGoogleAccount(profile.Account), profile.GoogleCookies, state, open)
	}

	if selected >= 0 && selected < len(profiles) {
		fmt.Fprintf(&out, "\nSeleccionado: **%s**\n", profiles[selected].Name)
	}

	out.WriteString("\nElige un perfil y pulsa **Cerrar seleccionado**, o **Cerrar todas** para cerrar todas las sesiones de Google.\n")
	out.WriteString("Se borran solo las cookies de Google y YouTube; el resto de sitios conserva su sesión.\n")

	return util.SafeCut(out.String(), 1900), ui.GooglePanel(selected, options)
}

func googleResultView(report win32.GoogleCloseReport) string {
	var out strings.Builder

	failed := 0
	for _, result := range report.Results {
		if result.Err != "" {
			failed++
		}
	}

	if len(report.Results) == 0 {
		out.WriteString("ℹ️ **Sin perfiles de Google**\n\n")
		out.WriteString("No se encontró ningún perfil de navegador Chromium con datos.\n")
		return util.SafeCut(out.String(), 1900)
	}

	if failed == 0 {
		out.WriteString("✅ **Sesiones de Google cerradas**\n\n")
	} else {
		out.WriteString("⚠️ **Cierre parcial**\n\n")
	}

	for _, result := range report.Results {
		account := win32.FormatGoogleAccount(result.Account)
		switch {
		case result.Err != "":
			fmt.Fprintf(&out, "• **%s** (%s) — ❌ %s\n", result.Profile, account, result.Err)
		case result.BeforeCookies == 0 && result.AfterCookies == 0:
			fmt.Fprintf(&out, "• **%s** (%s) — sin cookies de Google\n", result.Profile, account)
		default:
			verified := "✔ verificado"
			if result.AfterCookies > 0 {
				verified = fmt.Sprintf("⚠️ quedan %d", result.AfterCookies)
			}
			fmt.Fprintf(&out, "• **%s** (%s) — %d → %d cookies Google — %s\n",
				result.Profile, account, result.BeforeCookies, result.AfterCookies, verified)
		}
	}

	fmt.Fprintf(&out, "\nProcesos cerrados: **%d** · Navegadores restantes: **%d** · Tiempo: **%d ms**\n",
		report.Killed, report.Remaining, report.Elapsed.Milliseconds())

	return util.SafeCut(out.String(), 1900)
}
