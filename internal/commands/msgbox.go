//go:build windows

package commands

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"golang.org/x/sys/windows"
)

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

func MsgBox(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	title := StringOption(i, "title")
	text := StringOption(i, "text")
	icon := StringOption(i, "icon")

	if isBlank(text) {
		Finish(s, i, "❌ Falta el texto del mensaje.")
		return
	}

	flags := uintptr(0x00040000 | 0x00010000)
	switch icon {
	case "warn":
		flags |= 0x00000030
	case "error":
		flags |= 0x00000010
	default:
		flags |= 0x00000040
	}

	textUTF16, err := windows.UTF16FromString(text)
	if err != nil {
		Finish(s, i, "❌ Texto inválido: "+err.Error())
		return
	}
	titleUTF16, err := windows.UTF16FromString(title)
	if err != nil {
		Finish(s, i, "❌ Título inválido: "+err.Error())
		return
	}

	go func() {
		procMessageBoxW.Call(
			0,
			uintptr(unsafe.Pointer(&textUTF16[0])),
			uintptr(unsafe.Pointer(&titleUTF16[0])),
			flags,
		)
		runtime.KeepAlive(textUTF16)
		runtime.KeepAlive(titleUTF16)
	}()

	Finish(s, i, fmt.Sprintf("✅ MessageBox enviado: **%s**", truncate(title, 80)))
}
