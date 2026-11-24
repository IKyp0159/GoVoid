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

type heartbeat struct {
	PID     int   `json:"pid"`
	Updated int64 `json:"updated_unix"`
}

func HeartbeatPath() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, config.DataDirName, "agent.alive")
}

func WriteHeartbeat() error {
	path := HeartbeatPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(heartbeat{PID: os.Getpid(), Updated: time.Now().Unix()})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

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
