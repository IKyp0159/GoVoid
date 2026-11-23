package ui

import (
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

// Discord rechaza `"emoji":{}` en botones y opciones de select. discordgo
// v0.27.1 declara esos campos sin omitempty, así que el valor cero viaja en el
// JSON y la API responde COMPONENT_INVALID_EMOJI. Los tipos de este archivo
// emiten exactamente los campos que la API acepta.

type wireButton struct {
	Label    string
	Style    discordgo.ButtonStyle
	CustomID string
	Disabled bool
}

func (wireButton) Type() discordgo.ComponentType { return discordgo.ButtonComponent }

func (b wireButton) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     discordgo.ComponentType `json:"type"`
		Label    string                  `json:"label"`
		Style    discordgo.ButtonStyle   `json:"style"`
		CustomID string                  `json:"custom_id,omitempty"`
		Disabled bool                    `json:"disabled,omitempty"`
	}{
		Type:     discordgo.ButtonComponent,
		Label:    b.Label,
		Style:    b.Style,
		CustomID: b.CustomID,
		Disabled: b.Disabled,
	})
}

type wireOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type wireSelect struct {
	CustomID    string
	Placeholder string
	MinValues   int
	MaxValues   int
	Options     []wireOption
}

func (wireSelect) Type() discordgo.ComponentType {
	return discordgo.ComponentType(discordgo.StringSelectMenu)
}

func (s wireSelect) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type        discordgo.ComponentType `json:"type"`
		CustomID    string                  `json:"custom_id"`
		Placeholder string                  `json:"placeholder,omitempty"`
		MinValues   int                     `json:"min_values,omitempty"`
		MaxValues   int                     `json:"max_values,omitempty"`
		Options     []wireOption            `json:"options"`
	}{
		Type:        discordgo.ComponentType(discordgo.StringSelectMenu),
		CustomID:    s.CustomID,
		Placeholder: s.Placeholder,
		MinValues:   s.MinValues,
		MaxValues:   s.MaxValues,
		Options:     s.Options,
	})
}

// Button construye un botón válido para la API.
func Button(label string, style discordgo.ButtonStyle, customID string) discordgo.MessageComponent {
	return wireButton{Label: label, Style: style, CustomID: customID}
}

// ControlPanel devuelve las tres filas de botones del panel de control.
func ControlPanel() []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("📸 Screenshot", discordgo.PrimaryButton, "btn_screenshot"),
			Button("💻 SysInfo", discordgo.PrimaryButton, "btn_sysinfo"),
			Button("📋 Clipboard", discordgo.PrimaryButton, "btn_clipboard"),
			Button("🔑 Keylog Dump", discordgo.PrimaryButton, "btn_keylog_dump"),
		}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("⏻ Kill Agent", discordgo.DangerButton, "btn_kill"),
			Button("🔄 Restart", discordgo.DangerButton, "btn_restart"),
			Button("📂 Processes", discordgo.DangerButton, "btn_ps"),
			Button("💀 BSOD", discordgo.DangerButton, "btn_bsod"),
		}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("🔌 Shutdown Agent", discordgo.SecondaryButton, "btn_shutdown"),
			Button("📡 Reconectar", discordgo.SecondaryButton, "btn_reconnect"),
			Button("🛡️ Elevate", discordgo.SecondaryButton, "btn_elevate"),
		}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("▶️ Start Keylog", discordgo.SuccessButton, "btn_keylog_start"),
			Button("⏹ Stop Keylog", discordgo.SecondaryButton, "btn_keylog_stop"),
		}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("🔒 Lock Screen", discordgo.DangerButton, "btn_lock_on"),
			Button("🔓 Unlock", discordgo.SuccessButton, "btn_lock_off"),
		}},
	}
}

// ProcessSelect construye la fila con el menú de procesos de una página.
func ProcessSelect(page int, options []discordgo.SelectMenuOption) []discordgo.MessageComponent {
	if len(options) > 25 {
		options = options[:25]
	}
	converted := make([]wireOption, 0, len(options))
	for _, option := range options {
		converted = append(converted, wireOption{
			Label:       option.Label,
			Value:       option.Value,
			Description: option.Description,
		})
	}
	menu := wireSelect{
		CustomID:    fmt.Sprintf("sel_ps_%d", page),
		Placeholder: fmt.Sprintf("Selecciona un proceso — página %d", page+1),
		MinValues:   1,
		MaxValues:   1,
		Options:     converted,
	}
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{menu}},
	}
}

// ProcessNav construye la fila de navegación entre páginas de procesos.
func ProcessNav(page, totalPages int) []discordgo.MessageComponent {
	if totalPages <= 1 {
		return nil
	}
	prev := wireButton{
		Label:    "◀ Anterior",
		Style:    discordgo.SecondaryButton,
		CustomID: fmt.Sprintf("btn_ps_page_%d", page-1),
		Disabled: page <= 0,
	}
	next := wireButton{
		Label:    "Siguiente ▶",
		Style:    discordgo.SecondaryButton,
		CustomID: fmt.Sprintf("btn_ps_page_%d", page+1),
		Disabled: page >= totalPages-1,
	}
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{prev, next}},
	}
}
