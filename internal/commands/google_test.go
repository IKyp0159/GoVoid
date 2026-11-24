//go:build windows

package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/local/govoid/internal/win32"
)

func perfilesDePrueba() []win32.GoogleProfile {
	return []win32.GoogleProfile{
		{Name: "Chrome/Default", Account: "uno@gmail.com", GoogleCookies: 97, Session: true, Running: true},
		{Name: "Chrome/Profile 1", Account: "", GoogleCookies: 0, Session: false, Running: false},
	}
}

func TestGooglePanelViewListaPerfiles(t *testing.T) {
	content, components := googlePanelView(perfilesDePrueba(), 0)

	if !strings.Contains(content, "Chrome/Default") || !strings.Contains(content, "Chrome/Profile 1") {
		t.Fatalf("el panel no lista los perfiles:\n%s", content)
	}
	if !strings.Contains(content, "uno@gmail.com") {
		t.Fatalf("el panel no muestra la cuenta:\n%s", content)
	}
	if !strings.Contains(content, "sesión activa") {
		t.Fatalf("el panel no marca la sesión activa:\n%s", content)
	}
	if !strings.Contains(content, "Seleccionado: **Chrome/Default**") {
		t.Fatalf("el panel no marca la selección:\n%s", content)
	}
	if len(components) != 2 {
		t.Fatalf("esperaba 2 filas de componentes, llegaron %d", len(components))
	}
}

func TestGooglePanelViewSinPerfiles(t *testing.T) {
	content, components := googlePanelView(nil, -1)

	if !strings.Contains(content, "No se encontró ningún perfil") {
		t.Fatalf("mensaje vacío incorrecto:\n%s", content)
	}
	if len(components) != 1 {
		t.Fatalf("esperaba una fila de refresco, llegaron %d", len(components))
	}
}

func TestGoogleResultViewVerificado(t *testing.T) {
	report := win32.GoogleCloseReport{
		Results: []win32.GoogleCloseResult{
			{Profile: "Chrome/Default", Account: "uno@gmail.com", BeforeCookies: 97, AfterCookies: 0, Purged: true},
		},
		Killed:    57,
		Remaining: 0,
		Elapsed:   812 * time.Millisecond,
	}

	content := googleResultView(report)

	if !strings.Contains(content, "Sesiones de Google cerradas") {
		t.Fatalf("falta el título de éxito:\n%s", content)
	}
	if !strings.Contains(content, "97 → 0") || !strings.Contains(content, "✔ verificado") {
		t.Fatalf("falta la verificación por perfil:\n%s", content)
	}
	if !strings.Contains(content, "57") || !strings.Contains(content, "812 ms") {
		t.Fatalf("faltan las métricas:\n%s", content)
	}
}

func TestGoogleResultViewParcial(t *testing.T) {
	report := win32.GoogleCloseReport{
		Results: []win32.GoogleCloseResult{
			{Profile: "Chrome/Default", Account: "uno@gmail.com", BeforeCookies: 97, AfterCookies: 4, Err: "base de datos ocupada"},
		},
	}

	content := googleResultView(report)

	if !strings.Contains(content, "Cierre parcial") {
		t.Fatalf("falta el título de fallo:\n%s", content)
	}
	if !strings.Contains(content, "base de datos ocupada") {
		t.Fatalf("falta el error del perfil:\n%s", content)
	}
}

func TestGoogleIndex(t *testing.T) {
	if got := googleIndex("btn_google_one_3"); got != 3 {
		t.Fatalf("índice = %d, esperaba 3", got)
	}
	if got := googleIndex("btn_google_one_x"); got != -1 {
		t.Fatalf("índice = %d, esperaba -1", got)
	}
	if got := googleIndex("btn_google_all"); got != -1 {
		t.Fatalf("índice = %d, esperaba -1", got)
	}
}
