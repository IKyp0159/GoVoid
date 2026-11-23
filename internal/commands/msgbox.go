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

// MsgBox muestra un MessageBoxW en una goroutine separada para no bloquear
// el handler de interacciones.
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

	flags := uintptr(0x00040000 | 0x00010000) // MB_TOPMOST | MB_SETFOREGROUND
	switch icon {
	case "warn":
		flags |= 0x00000030 // MB_ICONWARNING
	case "error":
		flags |= 0x00000010 // MB_ICONERROR
	default:
		flags |= 0x00000040 // MB_ICONINFORMATION
	}

	// UTF16FromString devuelve la slice; el puntero se toma dentro de la
	// goroutine y la slice se queda capturada. UTF16PtrFromString habría dejado
	// un uintptr a memoria que el GC puede recoger mientras MessageBoxW espera.
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
		// La llamada bloquea hasta que el usuario cierra la ventana: por eso
		// vive fuera del handler, que ya respondió a Discord.
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
