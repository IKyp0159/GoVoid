//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procNtRaiseHardError        = ntdll.NewProc("NtRaiseHardError")
	procNtSetInformationProcess = ntdll.NewProc("NtSetInformationProcess")
	procRtlAdjustPrivilege      = ntdll.NewProc("RtlAdjustPrivilege")
	procAdjustTokenPrivileges   = advapi32.NewProc("AdjustTokenPrivileges")
	procIsProcessCritical       = kernel32.NewProc("IsProcessCritical")
)

const processBreakOnTermination = 29

var bsodTrace = func(string) {}

func SetBSODTrace(fn func(string)) {
	if fn == nil {
		bsodTrace = func(string) {}
		return
	}
	bsodTrace = fn
}

var criticalProcesses = []string{
	"csrss.exe",
	"smss.exe",
	"wininit.exe",
}

var criticalServices = []string{
	"RpcSs",
	"RpcEptMapper",
	"Power",
	"PlugPlay",
	"SystemEventsBroker",
	"BrokerInfrastructure",
	"LSM",
}

const (
	statusAssertionFailure = 0xC0000420

	responseOptionShutdown = 6

	seShutdownPrivilege = 19

	criticalExitWait = 1500 * time.Millisecond
)

type serviceHost struct {
	service string
	pid     uint32
}

func TriggerBSOD() (string, error) {
	bsodTrace("privilegio: activando SeDebugPrivilege")
	if err := enablePrivilege("SeDebugPrivilege"); err != nil {
		bsodTrace("privilegio: " + err.Error())
	}

	_ = MarkPendingBugcheck("proceso crítico → CRITICAL_PROCESS_DIED (0xEF)")

	var problems []string

	bsodTrace("autocrítico: marcando el proceso como crítico")
	if err := killSelfAsCritical(); err != nil {
		problems = append(problems, "autocrítico: "+err.Error())
		bsodTrace("autocrítico: " + err.Error())
	}

	bsodTrace("svchost: buscando proceso crítico")
	if err := killCriticalHosts(); err != nil {
		problems = append(problems, "proceso crítico: "+err.Error())
		bsodTrace("svchost: " + err.Error())
	}

	bsodTrace("NtRaiseHardError: disparando")
	if err := raiseHardError(); err == nil {
		return "NtRaiseHardError (0xC0000420)", nil
	} else {
		problems = append(problems, "NtRaiseHardError: "+err.Error())
		bsodTrace("NtRaiseHardError: " + err.Error())
	}

	for _, name := range criticalProcesses {
		pids, err := pidsByName(name)
		if err != nil || len(pids) == 0 {
			continue
		}
		for _, pid := range pids {
			if err := killProcess(pid); err != nil {
				problems = append(problems, fmt.Sprintf("%s (pid %d): %v", name, pid, err))
				continue
			}
			return fmt.Sprintf("%s (pid %d)", name, pid), nil
		}
	}

	_ = ClearPendingBugcheck()
	return "", errors.New(strings.Join(problems, "; "))
}

func killSelfAsCritical() error {
	previous, err := setProcessCritical(true)
	if err != nil {
		return err
	}
	bsodTrace(fmt.Sprintf("autocrítico: BreakOnTermination activo (antes=%v)", previous))

	if err := windows.TerminateProcess(windows.CurrentProcess(), 1); err != nil {
		if _, revertErr := setProcessCritical(previous); revertErr != nil {
			bsodTrace("autocrítico: NO se pudo revertir BreakOnTermination: " + revertErr.Error())
		}
		return fmt.Errorf("TerminateProcess: %w", err)
	}

	if _, revertErr := setProcessCritical(previous); revertErr != nil {
		bsodTrace("autocrítico: NO se pudo revertir BreakOnTermination: " + revertErr.Error())
	}
	return errors.New("el kernel no aplicó BreakOnTermination")
}

func setProcessCritical(critical bool) (previous bool, err error) {
	if err := procNtSetInformationProcess.Find(); err != nil {
		return false, fmt.Errorf("NtSetInformationProcess: %w", err)
	}
	if flag, known := isProcessCritical(windows.CurrentProcess()); known {
		previous = flag
	}

	value := int32(0)
	if critical {
		value = 1
	}
	status, _, _ := procNtSetInformationProcess.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(processBreakOnTermination),
		uintptr(unsafe.Pointer(&value)),
		unsafe.Sizeof(value))
	if status != 0 {
		return previous, fmt.Errorf("NtSetInformationProcess(29): 0x%X", uint32(status))
	}
	return previous, nil
}

func criticalServiceHosts() ([]serviceHost, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, fmt.Errorf("SCM: %w", err)
	}
	defer func() { _ = windows.CloseServiceHandle(manager) }()

	var hosts []serviceHost
	seen := make(map[uint32]bool)
	var lastErr error

	for _, name := range criticalServices {
		ptr, err := windows.UTF16PtrFromString(name)
		if err != nil {
			continue
		}
		service, err := windows.OpenService(manager, ptr, windows.SERVICE_QUERY_STATUS)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", name, err)
			continue
		}

		var status windows.SERVICE_STATUS_PROCESS
		var needed uint32
		err = windows.QueryServiceStatusEx(service, windows.SC_STATUS_PROCESS_INFO,
			(*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed)
		_ = windows.CloseServiceHandle(service)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", name, err)
			continue
		}
		if status.ProcessId == 0 || seen[status.ProcessId] {
			continue
		}
		seen[status.ProcessId] = true
		hosts = append(hosts, serviceHost{service: name, pid: status.ProcessId})
	}

	if len(hosts) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("ningún servicio crítico en ejecución")
	}
	return hosts, nil
}

func killCriticalHosts() error {
	hosts, err := criticalServiceHosts()
	if err != nil {
		return err
	}

	var last error
	for _, host := range hosts {
		handle, err := windows.OpenProcess(
			windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, host.pid)
		if err != nil {
			last = fmt.Errorf("%s (pid %d): %w", host.service, host.pid, err)
			continue
		}

		critical, known := isProcessCritical(handle)
		if known && !critical {
			_ = windows.CloseHandle(handle)
			last = fmt.Errorf("%s (pid %d) no lleva BreakOnTermination", host.service, host.pid)
			continue
		}

		err = windows.TerminateProcess(handle, 1)
		_ = windows.CloseHandle(handle)
		if err != nil {
			last = fmt.Errorf("%s (pid %d): %w", host.service, host.pid, err)
			continue
		}

		time.Sleep(criticalExitWait)
		last = fmt.Errorf("%s (pid %d) terminado sin bugcheck", host.service, host.pid)
	}

	if last == nil {
		last = errors.New("no hay procesos críticos accesibles")
	}
	return last
}

func isProcessCritical(handle windows.Handle) (critical, known bool) {
	if err := procIsProcessCritical.Find(); err != nil {
		return false, false
	}
	var flag int32
	result, _, _ := procIsProcessCritical.Call(uintptr(handle), uintptr(unsafe.Pointer(&flag)))
	if result == 0 {
		return false, false
	}
	return flag != 0, true
}

func raiseHardError() error {
	if err := enablePrivilege("SeShutdownPrivilege"); err != nil {
		return err
	}
	present, enabled, err := privilegeState("SeShutdownPrivilege")
	if err != nil {
		return err
	}
	if !present {
		return errors.New("el token no tiene SeShutdownPrivilege")
	}
	if !enabled {
		return errors.New("SeShutdownPrivilege no quedó activo")
	}

	var previous uint32
	procRtlAdjustPrivilege.Call(
		uintptr(seShutdownPrivilege), 1, 0, uintptr(unsafe.Pointer(&previous)))

	var response uint32
	status, _, _ := procNtRaiseHardError.Call(
		uintptr(statusAssertionFailure),
		0, 0, 0,
		uintptr(responseOptionShutdown),
		uintptr(unsafe.Pointer(&response)))
	if status != 0 {
		return fmt.Errorf("ntdll devolvió 0x%X", uint32(status))
	}
	return nil
}

func enablePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer func() { _ = token.Close() }()

	var luid windows.LUID
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	if err := windows.LookupPrivilegeValue(nil, ptr, &luid); err != nil {
		return err
	}

	state := windows.Tokenprivileges{PrivilegeCount: 1}
	state.Privileges[0].Luid = luid
	state.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED

	result, _, callErr := procAdjustTokenPrivileges.Call(
		uintptr(token), 0, uintptr(unsafe.Pointer(&state)), 0, 0, 0)
	if result == 0 {
		if callErr == nil {
			return fmt.Errorf("%s: AdjustTokenPrivileges falló sin código", name)
		}
		return fmt.Errorf("%s: %w", name, callErr)
	}
	if callErr == windows.ERROR_NOT_ALL_ASSIGNED {
		return fmt.Errorf("el token no tiene %s", name)
	}

	present, enabled, err := privilegeState(name)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("el token no tiene %s", name)
	}
	if !enabled {
		return fmt.Errorf("%s no quedó activo", name)
	}
	return nil
}

func privilegeState(name string) (present, enabled bool, err error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false, false, err
	}
	defer func() { _ = token.Close() }()

	var luid windows.LUID
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, false, err
	}
	if err := windows.LookupPrivilegeValue(nil, ptr, &luid); err != nil {
		return false, false, err
	}

	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if size == 0 {
		return false, false, errors.New("token sin información de privilegios")
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buf[0], size, &size); err != nil {
		return false, false, err
	}

	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	for _, entry := range privileges.AllPrivileges() {
		if entry.Luid == luid {
			return true, entry.Attributes&windows.SE_PRIVILEGE_ENABLED != 0, nil
		}
	}
	return false, false, nil
}

func pidsByName(name string) ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := windows.CloseHandle(snapshot); cerr != nil {
			_ = cerr
		}
	}()

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}

	var out []uint32
	for {
		if entry.ProcessID != 0 && equalFoldUTF16(entry.ExeFile[:], name) {
			out = append(out, entry.ProcessID)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return out, nil
}

func equalFoldUTF16(raw []uint16, want string) bool {
	got := windows.UTF16ToString(raw)
	if len(got) != len(want) {
		return false
	}
	for index := 0; index < len(got); index++ {
		left := got[index]
		right := want[index]
		if left >= 'A' && left <= 'Z' {
			left += 'a' - 'A'
		}
		if right >= 'A' && right <= 'Z' {
			right += 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}

func killProcess(pid uint32) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := windows.CloseHandle(handle); cerr != nil {
			_ = cerr
		}
	}()

	if err := windows.TerminateProcess(handle, 1); err != nil {
		return err
	}
	return nil
}

func TerminatePID(pid uint32) error {
	_ = enablePrivilege("SeDebugPrivilege")
	if pid == 0 {
		return errors.New("pid 0 no es un proceso")
	}
	return killProcess(pid)
}

func BSODDiagnostics() string {
	var report strings.Builder

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	fmt.Fprintf(&report, "proceso    : %s (pid %d)\n", filepath.Base(exe), os.Getpid())

	var token windows.Token
	identity := "sin token"
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err == nil {
		identity = integrityLevel(token)
		if token.IsElevated() {
			identity += ", elevado"
		} else {
			identity += ", sin elevar"
		}
		_ = token.Close()
	}
	fmt.Fprintf(&report, "token      : %s\n", identity)

	for _, name := range []string{"SeDebugPrivilege", "SeShutdownPrivilege"} {
		short := strings.TrimSuffix(name, "Privilege")
		present, enabled, err := privilegeState(name)
		if err != nil {
			fmt.Fprintf(&report, "%-10s : %v\n", short, err)
			continue
		}
		note := ""
		if !enabled {
			if err := enablePrivilege(name); err != nil {
				note = " -> " + err.Error()
			} else {
				note = " -> activo"
			}
		}
		fmt.Fprintf(&report, "%-10s : presente=%v activo=%v%s\n", short, present, enabled, note)
	}

	hosts, err := criticalServiceHosts()
	if err != nil {
		fmt.Fprintf(&report, "SCM        : %v\n", err)
		report.WriteString("veredicto  : SIN bugcheck\n")
		return report.String()
	}
	fmt.Fprintf(&report, "SCM        : %d procesos críticos\n", len(hosts))

	ready := 0
	for _, host := range hosts {
		handle, err := windows.OpenProcess(
			windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, host.pid)
		if err != nil {
			fmt.Fprintf(&report, "  %-20s pid %-6d TERMINATE denegado: %v\n", host.service, host.pid, err)
			continue
		}
		critical, known := isProcessCritical(handle)
		_ = windows.CloseHandle(handle)
		if !known {
			fmt.Fprintf(&report, "  %-20s pid %-6d crítico=desconocido TERMINATE concedido\n", host.service, host.pid)
			ready++
			continue
		}
		fmt.Fprintf(&report, "  %-20s pid %-6d crítico=%v TERMINATE concedido\n", host.service, host.pid, critical)
		if critical {
			ready++
		}
	}

	if ready > 0 {
		report.WriteString("veredicto  : listo para CRITICAL_PROCESS_DIED (0xEF)\n")
	} else {
		report.WriteString("veredicto  : sin proceso crítico terminable\n")
	}
	return report.String()
}

func integrityLevel(token windows.Token) string {
	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size)
	if size == 0 {
		return "integridad desconocida"
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buf[0], size, &size); err != nil {
		return "integridad desconocida"
	}

	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	sid := label.Label.Sid
	if sid == nil {
		return "integridad desconocida"
	}
	count := sid.SubAuthorityCount()
	if count == 0 {
		return "integridad desconocida"
	}

	switch rid := sid.SubAuthority(uint32(count) - 1); {
	case rid >= 0x4000:
		return "integridad System"
	case rid >= 0x3000:
		return "integridad Alta"
	case rid >= 0x2000:
		return "integridad Media"
	case rid >= 0x1000:
		return "integridad Baja"
	default:
		return "integridad Sin privilegios"
	}
}
