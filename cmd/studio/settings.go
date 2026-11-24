//go:build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cryptProtectUIForbidden = 0x1

type Settings struct {
	Version   int    `json:"version"`
	Token     string `json:"token,omitempty"`
	Guild     string `json:"guild"`
	Webhook   string `json:"webhook"`
	ShowToken bool   `json:"show_token"`
}

func settingsDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			base = dir
		}
	}
	return filepath.Join(base, "GoVoidStudio")
}

func defaultSettingsPath() string {
	return filepath.Join(settingsDir(), "studio.json")
}

func trimBOM(payload []byte) []byte {
	if len(payload) >= 3 && payload[0] == 0xEF && payload[1] == 0xBB && payload[2] == 0xBF {
		return payload[3:]
	}
	return payload
}

func protect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, cryptProtectUIForbidden, &output); err != nil {
		return nil, err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data))) }()
	plain := unsafe.Slice(output.Data, output.Size)
	return append([]byte(nil), plain...), nil
}

func unprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, cryptProtectUIForbidden, &output); err != nil {
		return nil, err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data))) }()
	plain := unsafe.Slice(output.Data, output.Size)
	return append([]byte(nil), plain...), nil
}

func encodeSecret(token string) string {
	if token == "" {
		return ""
	}
	sealed, err := protect([]byte(token))
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(sealed)
}

func decodeSecret(encoded string) string {
	if encoded == "" {
		return ""
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	plain, err := unprotect(sealed)
	if err != nil {
		return ""
	}
	return string(plain)
}

func (a *App) loadSettings() {
	settings := Settings{Version: 1}

	payload, err := os.ReadFile(defaultSettingsPath())
	if err == nil {
		var loaded Settings
		if jsonErr := json.Unmarshal(trimBOM(payload), &loaded); jsonErr == nil {
			settings = loaded
		} else {
			a.logLine("configuración ilegible, se empieza limpio: %v", jsonErr)
		}
	}

	a.setField("guild", settings.Guild)
	a.setField("webhook", settings.Webhook)

	token := decodeSecret(settings.Token)
	if settings.Token != "" && token == "" {
		a.logLine("el token guardado no se pudo descifrar en esta cuenta; vuelve a pegarlo")
	}
	a.setField("token", token)
	a.setChecked("showToken", settings.ShowToken)
	a.applyTokenMask(settings.ShowToken)
}

func (a *App) collectSettings() Settings {
	return Settings{
		Version:   1,
		Token:     encodeSecret(strings.TrimSpace(a.field("token"))),
		Guild:     strings.TrimSpace(a.field("guild")),
		Webhook:   strings.TrimSpace(a.field("webhook")),
		ShowToken: a.checked("showToken"),
	}
}

func (a *App) saveSettings(quiet bool) {
	payload, err := json.MarshalIndent(a.collectSettings(), "", "  ")
	if err == nil {
		err = os.MkdirAll(settingsDir(), 0o700)
		if err == nil {
			err = os.WriteFile(defaultSettingsPath(), payload, 0o600)
		}
	}
	if err != nil {
		a.logLine("no se pudo guardar la configuración: %v", err)
		if !quiet {
			a.message(mbIconError, "No se pudo guardar la configuración:\n%v", err)
		}
		return
	}
	if !quiet {
		a.setStatus("Configuración guardada")
		a.logLine("configuración guardada: %s", defaultSettingsPath())
	}
}

func (a *App) openSettings() {
	path := a.askOpenFile("Abrir configuración", "Configuración de GoVoid Studio (*.json)\x00*.json\x00Todos los archivos (*.*)\x00*.*\x00")
	if path == "" {
		return
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		a.message(mbIconError, "No se pudo leer %s:\n%v", path, err)
		return
	}
	var settings Settings
	if err := json.Unmarshal(trimBOM(payload), &settings); err != nil {
		a.message(mbIconError, "El archivo no es una configuración válida:\n%v", err)
		return
	}
	a.setField("guild", settings.Guild)
	a.setField("webhook", settings.Webhook)
	a.setField("token", decodeSecret(settings.Token))
	a.setChecked("showToken", settings.ShowToken)
	a.applyTokenMask(settings.ShowToken)
	a.updateSummary()
	a.logLine("configuración cargada: %s", path)
	a.setStatus("Configuración cargada desde " + filepath.Base(path))
}

func (a *App) applyTokenMask(show bool) {
	token := a.fields["token"]
	if token == 0 {
		return
	}
	mask := uintptr(0x25CF)
	if show {
		mask = 0
	}
	procSendMessage.Call(token, 0x00CC, mask, 0)
	procInvalidateRect.Call(token, 0, 1)
}
