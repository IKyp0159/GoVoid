//go:build windows

package win32

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/local/govoid/internal/config"
)

// heartbeat es la prueba de vida que deja el agente conectado. Una copia que
// pierde el mutex la lee para distinguir "ya hay un agente sano" de "hay un
// proceso colgado que no suelta el nombre".
type heartbeat struct {
	PID     int   `json:"pid"`
	Updated int64 `json:"updated_unix"`
}

// HeartbeatPath vive junto al diario, machine-wide, para que lo vean tanto la
// copia de Administrator como la de SYSTEM.
func HeartbeatPath() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, config.DataDirName, "agent.alive")
}

// WriteHeartbeat marca el agente como vivo. Se llama al conectar y cada 30 s
// desde el latido; sin esto una copia manual no puede saber si la instancia en
// marcha está sana o se quedó a medias.
func WriteHeartbeat() error {
	path := HeartbeatPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(heartbeat{PID: os.Getpid(), Updated: time.Now().Unix()})
	if err != nil {
		return err
	}
	// Escritura atómica: un lector nunca ve un archivo a medias.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HeartbeatFresh indica si la instancia en marcha late dentro de maxAge.
func HeartbeatFresh(maxAge time.Duration) bool {
	raw, err := os.ReadFile(HeartbeatPath())
	if err != nil {
		return false
	}
	var hb heartbeat
	if err := json.Unmarshal(raw, &hb); err != nil {
		return false
	}
	return time.Since(time.Unix(hb.Updated, 0)) <= maxAge
}

// TakeOverStaleAgent termina las otras copias del agente y devuelve cuántas
// mató. Solo se llama cuando el mutex está tomado y el latido está viejo: la
// instancia en marcha no está conectada, así que el arranque manual debe ganar
// en vez de salir en silencio.
func TakeOverStaleAgent() int {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	self := uint32(os.Getpid())
	want := strings.ToLower(config.BinaryName)
	killed := 0

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0
	}

	for {
		pid := entry.ProcessID
		if pid != 0 && pid != self {
			name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
			if name == want {
				terminateProcess(pid)
				killed++
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return killed
		}
	}
}
