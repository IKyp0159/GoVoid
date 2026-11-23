//go:build windows

package win32

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// escapeTools son los ejecutables con los que se puede abrir una brecha desde
// el escritorio bloqueado. El candado los mata en cuanto aparecen: no puede
// impedir que se lancen desde el escritorio seguro de Ctrl+Alt+Del, pero sí
// puede impedir que sobrevivan el tiempo suficiente para hacer algo.
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

// killEscapeTools recorre la lista de procesos y termina los que están en la
// lista negra. Se llama una vez por segundo desde el temporizador del candado,
// en el hilo de la ventana; la enumeración es corta y no toca el input.
//
// sc.exe, taskkill.exe, reg.exe y net.exe no están en la lista: son las
// herramientas con las que el propio candado instala y retira sus capas, y
// matarlas dejaría el desbloqueo a medias. Detrás de ellos hace falta una
// consola, y las consolas sí se matan.
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
