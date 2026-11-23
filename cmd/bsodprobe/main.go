//go:build windows

// Comando de prueba del bugcheck. Corre el mismo TriggerBSOD que usa /bsod,
// pero desde una consola con el resultado a la vista y a un archivo, para saber
// si el camino destructivo devuelve error, mata el proceso o tumba la máquina.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/local/govoid/internal/win32"
)

func main() {
	path := filepath.Join(os.Getenv("ProgramData"), "GoVoid", "probe.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	file, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)

	say := func(format string, args ...any) {
		line := time.Now().Format("2006-01-02 15:04:05.000") + " " + fmt.Sprintf(format, args...) + "\n"
		_, _ = os.Stdout.WriteString(line)
		if file != nil {
			_, _ = file.WriteString(line)
			_ = file.Sync()
		}
	}

	say("probe: arranque pid=%d", os.Getpid())

	// Modo de solo lectura: informa qué bugcheck quedó registrado y si hay un
	// aviso pendiente, sin tocar el token ni disparar nada.
	if len(os.Args) > 1 && os.Args[1] == "check" {
		code, when, err := win32.LastBugcheckCode()
		say("check: codigo=%q hora=%q err=%v", code, when.Format(time.RFC3339), err)
		if method, pending := win32.PendingBugcheck(); pending {
			say("check: aviso pendiente=%q", method)
		} else {
			say("check: sin aviso pendiente")
		}
		_ = file.Close()
		return
	}

	// La traza va al mismo archivo con Sync por línea: si la máquina cae a
	// mitad del disparo, la última línea escrita dice qué paso la tumbó.
	win32.SetBSODTrace(func(line string) { say("traza: %s", line) })

	say("probe: diagnostico\n%s", win32.BSODDiagnostics())
	say("probe: disparando TriggerBSOD")

	method, err := win32.TriggerBSOD()
	say("probe: volvió method=%q err=%v", method, err)
	if file != nil {
		_ = file.Close()
	}
}
