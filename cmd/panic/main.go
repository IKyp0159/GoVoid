//go:build windows

// Comando panic: botón de emergencia. Mata todo lo que el agente haya dejado
// corriendo y borra sus capas de persistencia.
//
// Es deliberadamente autónomo. No importa internal/win32 ni internal/config:
// una herramienta de rescate no puede depender de aquello de lo que rescata.
// Se compila una vez, se guarda en un USB y sigue funcionando aunque el
// proyecto desaparezca del disco.
//
// No toca el ejecutable original desde el que se compiló: el objetivo es dejar
// la máquina limpia, no borrar la evidencia.
package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// --- qué se considera parte del RAT ------------------------------------------

// ratNames son los nombres de proceso que deja el agente: la copia instalada,
// los cuatro centinelas, y los binarios de desarrollo o de cliente.
var ratNames = []string{
	"svchost32.exe",
	"agent.exe",
	"agent_dbg.exe",
	"g0.exe", "g1.exe", "g2.exe", "g3.exe",
	"winhelp.exe",
	"benchcheck.exe",
}

// knownServices y knownTasks cubren los nombres por defecto y los derivados de
// los clientes compilados hasta ahora. La búsqueda por ruta y por línea de
// comandos pilla cualquier otro.
var (
	knownServices = []string{"GoVoidShield", "WinHelpShield", "BenchCheckShield"}
	knownTasks    = []string{
		"WindowsDefenderUpdate", "WindowsDefenderUpdateBoot",
		"GoVoidShieldGuard", "WinHelpShieldGuard", "BenchCheckShieldGuard",
		"WinHelpUpdate", "WinHelpUpdateBoot",
		"BenchCheckUpdate", "BenchCheckUpdateBoot",
	}
)

// logonHelpers son las puertas de la pantalla de inicio que el agente ciega con
// un Debugger en IFEO.
var logonHelpers = []string{
	"sethc.exe", "utilman.exe", "osk.exe", "narrator.exe",
	"magnify.exe", "AtBroker.exe", "DisplaySwitch.exe",
}

// --- estado de la ejecución ---------------------------------------------------

var (
	verbose    bool
	forceFiles bool
	report     []string
	failures   int
	// paths de los procesos que matamos: son copias del agente con prueba de
	// procedencia, no hay que adivinar por nombre.
	killedProcessPaths []string
)

func killedPaths() []string {
	return killedProcessPaths
}

func main() {
	var (
		dryRun   = flag.Bool("dry-run", false, "enseña lo que haría sin tocar nada")
		elevated = flag.Bool("elevated", false, "uso interno: ya se relanzó como administrador")
		keepData = flag.Bool("keep-data", false, "no borra las carpetas de datos, solo los procesos y las capas")
	)
	flag.BoolVar(&verbose, "v", false, "detalle de cada paso")
	flag.BoolVar(&forceFiles, "force-files", false,
		"borra los archivos de las rutas de siembra aunque no se pruebe que son del agente")
	flag.Parse()

	if !*dryRun && !*elevated && !isElevated() {
		if err := relaunchElevated(); err != nil {
			fmt.Printf("no se pudo pedir elevación: %v\n", err)
			fmt.Println("ejecútalo con botón derecho -> Ejecutar como administrador")
		}
		return
	}

	fmt.Println("panic — limpieza del agente")
	fmt.Println("===========================")
	if *dryRun {
		fmt.Println("modo simulación: no se cambia nada")
	}
	fmt.Println()

	pause := !*dryRun
	killProcesses(*dryRun)
	cleanServices(*dryRun)
	cleanTasks(*dryRun)
	restoreWinlogon(*dryRun)
	cleanIFEO(*dryRun)
	cleanPolicies(*dryRun)
	cleanRunKeys(*dryRun)
	restoreInputDevices(*dryRun)
	if !*keepData {
		cleanFiles(*dryRun)
	}

	fmt.Println()
	fmt.Println("resumen")
	fmt.Println("-------")
	if len(report) == 0 {
		fmt.Println("  nada que quitar: la máquina ya estaba limpia")
	} else {
		for _, line := range report {
			fmt.Println("  " + line)
		}
	}
	if failures > 0 {
		fmt.Printf("\n  %d paso(s) fallaron; revisa los mensajes de arriba\n", failures)
	}

	if pause {
		pauseIfInteractive()
	}
}

func note(entry string) {
	report = append(report, entry)
}

func fail(format string, args ...any) {
	failures++
	fmt.Printf("  ! "+format+"\n", args...)
}

func detail(format string, args ...any) {
	if verbose {
		fmt.Printf("  · "+format+"\n", args...)
	}
}

// --- procesos ----------------------------------------------------------------

func killProcesses(dryRun bool) {
	fmt.Println("[1/9] procesos")

	self := uint32(os.Getpid())
	entries, err := listProcesses()
	if err != nil {
		fail("no se pudo enumerar procesos: %v", err)
		return
	}

	killed := 0
	for _, entry := range entries {
		if entry.pid == self || entry.pid == 0 {
			continue
		}
		if !isRATProcess(entry.name, entry.path) {
			continue
		}
		if dryRun {
			fmt.Printf("  mataría %-16s pid=%-6d %s\n", entry.name, entry.pid, entry.path)
			rememberPath(entry.path)
			killed++
			continue
		}
		if err := terminate(entry.pid); err != nil {
			fail("no se pudo matar %s (pid %d): %v", entry.name, entry.pid, err)
			continue
		}
		fmt.Printf("  matado %-16s pid=%d\n", entry.name, entry.pid)
		rememberPath(entry.path)
		killed++
	}

	if killed == 0 {
		fmt.Println("  ninguno vivo")
	} else {
		note(fmt.Sprintf("procesos: %d terminados", killed))
	}
}

type procEntry struct {
	pid  uint32
	name string
	path string
}

// rememberPath guarda la ruta de un ejecutable que acabamos de matar. Es la
// única prueba de procedencia que no depende de adivinar por nombre: el
// archivo estaba corriendo, luego es una copia del agente.
func rememberPath(path string) {
	if path == "" {
		return
	}
	for _, known := range killedProcessPaths {
		if strings.EqualFold(known, path) {
			return
		}
	}
	killedProcessPaths = append(killedProcessPaths, path)
}

func listProcesses() ([]procEntry, error) {
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

	var out []procEntry
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		out = append(out, procEntry{
			pid:  entry.ProcessID,
			name: name,
			path: processPath(entry.ProcessID),
		})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return out, nil
}

var procQueryFullProcessImageNameW = windows.NewLazySystemDLL("kernel32.dll").
	NewProc("QueryFullProcessImageNameW")

// processPath devuelve la ruta del ejecutable. Un proceso protegido o ya
// muerto devuelve cadena vacía: se ignora, no es un fallo.
func processPath(pid uint32) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer func() {
		if cerr := windows.CloseHandle(handle); cerr != nil {
			_ = cerr
		}
	}()

	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	ok, _, _ := procQueryFullProcessImageNameW.Call(
		uintptr(handle), 0,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:size])
}

func isRATProcess(name, path string) bool {
	lowerName := strings.ToLower(name)

	// Por ruta: los centinelas y las copias viven en la carpeta de datos.
	if path != "" {
		lowerPath := strings.ToLower(path)
		for _, dir := range dataDirs() {
			if strings.HasPrefix(lowerPath, strings.ToLower(dir)+`\`) {
				return true
			}
		}
		for _, target := range installTargets() {
			if strings.EqualFold(path, target) {
				return true
			}
		}
	}

	// Por nombre: solo los que el agente usa de verdad.
	for _, known := range ratNames {
		if lowerName == known {
			return true
		}
	}
	return false
}

func terminate(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := windows.CloseHandle(handle); cerr != nil {
			_ = cerr
		}
	}()
	return windows.TerminateProcess(handle, 1)
}

// --- servicios ----------------------------------------------------------------

func cleanServices(dryRun bool) {
	fmt.Println("[2/9] servicios")

	manager, err := mgr.Connect()
	if err != nil {
		fail("no se pudo abrir el SCM: %v", err)
		return
	}
	defer func() {
		if cerr := manager.Disconnect(); cerr != nil {
			_ = cerr
		}
	}()

	names, err := manager.ListServices()
	if err != nil {
		fail("no se pudo listar servicios: %v", err)
		return
	}

	removed := 0
	for _, name := range names {
		service, openErr := manager.OpenService(name)
		if openErr != nil {
			continue
		}

		wanted := containsFold(knownServices, name)
		if !wanted {
			if config, cfgErr := service.Config(); cfgErr == nil {
				wanted = mentionsRAT(config.BinaryPathName)
			}
		}
		if !wanted {
			_ = service.Close()
			continue
		}

		if dryRun {
			fmt.Printf("  borraría el servicio %s\n", name)
			_ = service.Close()
			removed++
			continue
		}

		_, _ = service.Control(svc.Stop)
		if delErr := service.Delete(); delErr != nil {
			fail("servicio %s: %v", name, delErr)
			_ = service.Close()
			continue
		}
		_ = service.Close()
		fmt.Printf("  borrado servicio %s\n", name)
		removed++
		// El SCM tarda en soltar el nombre; el proceso ya no existe.
	}

	if removed == 0 {
		fmt.Println("  ninguno")
	} else {
		note(fmt.Sprintf("servicios: %d borrados", removed))
	}
}

// --- tareas programadas -------------------------------------------------------

func cleanTasks(dryRun bool) {
	fmt.Println("[3/9] tareas programadas")

	// Solo se tocan las tareas que existen de verdad. Listar los nombres
	// conocidos a ciegas hacía que el informe anunciara tareas ya borradas.
	existing := existingTaskNames()

	names := map[string]struct{}{}
	for _, known := range knownTasks {
		if _, ok := existing[strings.ToLower(known)]; ok {
			names[strings.ToLower(known)] = struct{}{}
		}
	}
	for _, found := range scanTasks() {
		names[strings.ToLower(found)] = struct{}{}
	}

	if len(names) == 0 {
		fmt.Println("  ninguna")
		return
	}

	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	removed := 0
	for _, name := range ordered {
		if dryRun {
			fmt.Printf("  borraría la tarea %s\n", name)
			removed++
			continue
		}
		err := runHidden("schtasks.exe", "/delete", "/tn", name, "/f")
		if err != nil {
			// La tarea puede no existir: eso no es un fallo.
			detail("%s: %v", name, err)
			continue
		}
		fmt.Printf("  borrada tarea %s\n", name)
		removed++
	}

	if removed == 0 {
		fmt.Println("  ninguna")
	} else {
		note(fmt.Sprintf("tareas: %d borradas", removed))
	}
}

// existingTaskNames devuelve en minúsculas los nombres de tarea del sistema.
func existingTaskNames() map[string]struct{} {
	out := map[string]struct{}{}

	output, err := exec.Command("schtasks.exe", "/query", "/fo", "csv", "/nh").Output()
	if err != nil {
		detail("schtasks /query: %v", err)
		return out
	}

	reader := csv.NewReader(strings.NewReader(string(output)))
	reader.FieldsPerRecord = -1
	for {
		record, readErr := reader.Read()
		if readErr != nil {
			break
		}
		if len(record) == 0 {
			continue
		}
		name := strings.TrimSpace(record[0])
		name = strings.TrimPrefix(name, `\`)
		if name == "" {
			continue
		}
		out[strings.ToLower(name)] = struct{}{}
	}
	return out
}

// scanTasks busca tareas cuyo ejecutable sea el agente, aunque el nombre no
// esté en la lista. Así se pilla un cliente renombrado.
func scanTasks() []string {
	output, err := exec.Command("schtasks.exe", "/query", "/fo", "csv", "/v").Output()
	if err != nil {
		detail("schtasks /query: %v", err)
		return nil
	}

	reader := csv.NewReader(strings.NewReader(string(output)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return nil
	}

	header := records[0]
	nameColumn, runColumn := -1, -1
	for index, column := range header {
		switch strings.ToLower(strings.TrimSpace(column)) {
		case "taskname", "nombre de tarea":
			nameColumn = index
		case "task to run", "tarea que se ejecuta":
			runColumn = index
		}
	}
	if nameColumn < 0 || runColumn < 0 {
		return nil
	}

	var found []string
	for _, record := range records[1:] {
		if len(record) <= nameColumn || len(record) <= runColumn {
			continue
		}
		if !mentionsRAT(record[runColumn]) {
			continue
		}
		name := strings.TrimSpace(record[nameColumn])
		if name == "" || strings.EqualFold(name, "TaskName") {
			continue
		}
		found = append(found, strings.TrimPrefix(name, `\`))
	}
	return found
}

// --- registro ----------------------------------------------------------------

func restoreWinlogon(dryRun bool) {
	fmt.Println("[4/9] shell de Winlogon")

	const path = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		fail("Winlogon: %v", err)
		return
	}
	defer func() {
		if cerr := key.Close(); cerr != nil {
			_ = cerr
		}
	}()

	current, _, err := key.GetStringValue("Shell")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		fail("leer Shell: %v", err)
		return
	}

	restored := false
	values, _ := key.ReadValueNames(0)
	for _, name := range values {
		if !strings.HasSuffix(strings.ToLower(name), "shellbackup") {
			continue
		}
		previous, _, valueErr := key.GetStringValue(name)
		if valueErr != nil || strings.TrimSpace(previous) == "" {
			continue
		}
		if dryRun {
			fmt.Printf("  restauraría Shell desde %s: %s\n", name, previous)
			restored = true
			continue
		}
		if setErr := key.SetStringValue("Shell", previous); setErr != nil {
			fail("restaurar Shell: %v", setErr)
			continue
		}
		fmt.Printf("  Shell restaurado desde %s: %s\n", name, previous)
		restored = true
	}

	if !restored && mentionsRAT(current) {
		if dryRun {
			fmt.Println("  pondría Shell = explorer.exe")
		} else if setErr := key.SetStringValue("Shell", "explorer.exe"); setErr != nil {
			fail("Shell = explorer.exe: %v", setErr)
		} else {
			fmt.Println("  Shell = explorer.exe")
			restored = true
		}
	}

	if !dryRun {
		for _, name := range values {
			if strings.HasSuffix(strings.ToLower(name), "shellbackup") {
				_ = key.DeleteValue(name)
			}
		}
	}

	if !restored {
		fmt.Printf("  Shell = %q (correcto)\n", current)
	} else {
		note("winlogon: shell devuelto a explorer.exe")
	}
}

func cleanIFEO(dryRun bool) {
	fmt.Println("[5/9] puertas de la pantalla de inicio (IFEO)")

	const base = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options`

	touched := 0
	for _, helper := range logonHelpers {
		path := base + `\` + helper
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path,
			registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			continue // la clave no existe: esa puerta está sana
		}

		debugger, _, debugErr := key.GetStringValue("Debugger")
		if debugErr != nil || !mentionsRAT(debugger) && !strings.Contains(debugger, "--noop") {
			_ = key.Close()
			continue
		}

		if dryRun {
			fmt.Printf("  limpiaría %s -> %s\n", helper, debugger)
			touched++
			_ = key.Close()
			continue
		}

		if delErr := key.DeleteValue("Debugger"); delErr != nil &&
			!errors.Is(delErr, registry.ErrNotExist) {
			fail("%s: %v", helper, delErr)
			_ = key.Close()
			continue
		}
		purgeBackups(key)
		_ = key.Close()
		fmt.Printf("  %s limpio\n", helper)
		touched++
	}

	if touched == 0 {
		fmt.Println("  ninguna comprometida")
	} else {
		note(fmt.Sprintf("IFEO: %d puertas liberadas", touched))
	}
}

func cleanPolicies(dryRun bool) {
	fmt.Println("[6/9] políticas DisableCAD / DisableLockWorkstation")

	const path = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		fmt.Println("  clave absente")
		return
	}
	defer func() {
		if cerr := key.Close(); cerr != nil {
			_ = cerr
		}
	}()

	touched := 0
	for _, name := range []string{"DisableCAD", "DisableLockWorkstation"} {
		current, _, getErr := key.GetIntegerValue(name)
		if getErr != nil {
			continue
		}

		previous, hadPrevious, backupErr := key.GetIntegerValue("GoVoidBackup" + name)
		_ = hadPrevious

		if dryRun {
			fmt.Printf("  quitaría %s (valor %d)\n", name, current)
			touched++
			continue
		}

		if backupErr == nil {
			if setErr := key.SetDWordValue(name, uint32(previous)); setErr != nil {
				fail("%s: %v", name, setErr)
				continue
			}
		} else if delErr := key.DeleteValue(name); delErr != nil &&
			!errors.Is(delErr, registry.ErrNotExist) {
			fail("%s: %v", name, delErr)
			continue
		}
		fmt.Printf("  %s restaurado\n", name)
		touched++
	}

	if !dryRun {
		purgeBackups(key)
	}

	if touched == 0 {
		fmt.Println("  ninguna puesta")
	} else {
		note(fmt.Sprintf("políticas: %d restauradas", touched))
	}
}

func cleanRunKeys(dryRun bool) {
	fmt.Println("[7/9] claves Run")

	roots := []struct {
		name string
		key  registry.Key
	}{
		{"HKCU", registry.CURRENT_USER},
		{"HKLM", registry.LOCAL_MACHINE},
	}
	const path = `Software\Microsoft\Windows\CurrentVersion\Run`

	touched := 0
	for _, root := range roots {
		key, err := registry.OpenKey(root.key, path, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			continue
		}

		names, _ := key.ReadValueNames(0)
		for _, name := range names {
			value, _, valueErr := key.GetStringValue(name)
			if valueErr != nil {
				continue
			}
			if !mentionsRAT(value) && !containsFold(ratNames, strings.ToLower(filepath.Base(value))) {
				continue
			}

			if dryRun {
				fmt.Printf("  %s: quitaría %s -> %s\n", root.name, name, value)
				touched++
				continue
			}
			if delErr := key.DeleteValue(name); delErr != nil &&
				!errors.Is(delErr, registry.ErrNotExist) {
				fail("%s %s: %v", root.name, name, delErr)
				continue
			}
			fmt.Printf("  %s: quitado %s\n", root.name, name)
			touched++
		}
		_ = key.Close()
	}

	if touched == 0 {
		fmt.Println("  ninguna")
	} else {
		note(fmt.Sprintf("claves Run: %d quitadas", touched))
	}
}

// purgeBackups borra los valores de respaldo que dejó el agente en una clave.
func purgeBackups(key registry.Key) {
	names, _ := key.ReadValueNames(0)
	for _, name := range names {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "govoid") || strings.HasPrefix(lower, "winhelp") ||
			strings.HasPrefix(lower, "benchcheck") {
			_ = key.DeleteValue(name)
		}
	}
}

// --- hardware de entrada ------------------------------------------------------

var (
	guidDevClassKeyboard = guid{0x4D36E96B, 0xE325, 0x11CE,
		[8]byte{0xBF, 0xC1, 0x08, 0x00, 0x2B, 0xE1, 0x03, 0x18}}
	guidDevClassMouse = guid{0x4D36E96F, 0xE325, 0x11CE,
		[8]byte{0xBF, 0xC1, 0x08, 0x00, 0x2B, 0xE1, 0x03, 0x18}}
)

const (
	digcfPresent = 0x00000002

	difPropertyChange = 0x00000012

	dicsEnable = 0x00000001
	dicsGlobal = 0x00000001
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type spDevInfoData struct {
	Size      uint32
	ClassGUID guid
	DevInst   uint32
	Reserved  uintptr
}

type spClassInstallHeader struct {
	Size            uint32
	InstallFunction uint32
}

type spPropChangeParams struct {
	ClassInstallHeader spClassInstallHeader
	StateChange        uint32
	Scope              uint32
	HwProfile          uint32
}

var (
	setupapi = windows.NewLazySystemDLL("setupapi.dll")

	procSetupDiGetClassDevsW          = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInfo         = setupapi.NewProc("SetupDiEnumDeviceInfo")
	procSetupDiSetClassInstallParamsW = setupapi.NewProc("SetupDiSetClassInstallParamsW")
	procSetupDiCallClassInstaller     = setupapi.NewProc("SetupDiCallClassInstaller")
	procSetupDiDestroyDeviceInfoList  = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

// restoreInputDevices vuelve a habilitar teclados y ratones por si el candado
// los apagó a nivel PnP. Sin esto la máquina se queda sin entrada y esta
// herramienta también sería inútil, porque no habría teclado para lanzarla.
func restoreInputDevices(dryRun bool) {
	fmt.Println("[8/9] teclado y ratón")

	if dryRun {
		fmt.Printf("  habilitaría teclados=%d ratones=%d\n",
			countDevices(&guidDevClassKeyboard), countDevices(&guidDevClassMouse))
		return
	}

	keyboards := setDeviceState(&guidDevClassKeyboard, dicsEnable)
	mice := setDeviceState(&guidDevClassMouse, dicsEnable)

	fmt.Printf("  habilitados: %d teclado(s), %d ratón(es)\n", keyboards, mice)
	if keyboards+mice > 0 {
		note(fmt.Sprintf("hardware: %d dispositivo(s) reactivados", keyboards+mice))
	}
}

func countDevices(class *guid) int {
	devices, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(class)), 0, 0, digcfPresent)
	if devices == 0 || devices == ^uintptr(0) {
		return 0
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devices)

	count := 0
	for index := uintptr(0); ; index++ {
		var info spDevInfoData
		info.Size = uint32(unsafe.Sizeof(info))
		ok, _, _ := procSetupDiEnumDeviceInfo.Call(devices, index, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			return count
		}
		count++
	}
}

func setDeviceState(class *guid, state uint32) int {
	changed := applyDeviceState(class, state, digcfPresent)
	if changed > 0 {
		return changed
	}
	return applyDeviceState(class, state, 0)
}

func applyDeviceState(class *guid, state uint32, flags uintptr) int {
	devices, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(class)), 0, 0, flags)
	if devices == 0 || devices == ^uintptr(0) {
		return 0
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devices)

	changed := 0
	for index := uintptr(0); ; index++ {
		var info spDevInfoData
		info.Size = uint32(unsafe.Sizeof(info))
		ok, _, _ := procSetupDiEnumDeviceInfo.Call(devices, index, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			return changed
		}

		var params spPropChangeParams
		params.ClassInstallHeader.Size = uint32(unsafe.Sizeof(params.ClassInstallHeader))
		params.ClassInstallHeader.InstallFunction = difPropertyChange
		params.StateChange = state
		params.Scope = dicsGlobal

		set, _, _ := procSetupDiSetClassInstallParamsW.Call(
			devices, uintptr(unsafe.Pointer(&info)),
			uintptr(unsafe.Pointer(&params)), unsafe.Sizeof(params))
		if set == 0 {
			continue
		}
		applied, _, _ := procSetupDiCallClassInstaller.Call(
			difPropertyChange, devices, uintptr(unsafe.Pointer(&info)))
		if applied != 0 {
			changed++
		}
	}
}

// --- ficheros ----------------------------------------------------------------

// cleanFiles borra SOLO las copias que el agente sembró al ejecutarse.
//
// La regla es de procedencia, no de nombre: un archivo se borra si (a) estaba
// corriendo y lo matamos, o (b) está en una ruta de siembra y su contenido es
// un binario del agente. Un archivo llamado igual pero que no es el agente se
// conserva y se avisa. Así una copia del original que tengas guardada a mano
// no desaparece.
func cleanFiles(dryRun bool) {
	fmt.Println("[9/9] ficheros")

	candidates := fileCandidates()

	removed := 0
	kept := 0
	for _, target := range candidates {
		if !exists(target) {
			continue
		}
		if isSelf(target) {
			fmt.Printf("  conservado (es esta herramienta) %s\n", target)
			kept++
			continue
		}

		// El original y sus builds de desarrollo nunca se borran, ni siquiera
		// si estaban corriendo. Se matan, se dejan en el disco.
		if isOriginalName(target) && !forceFiles {
			fmt.Printf("  conservado (ejecutable original) %s\n", target)
			kept++
			continue
		}

		fromProcess := isKilledPath(target)
		agent, markerErr := isAgentBinary(target)
		if markerErr != nil {
			// Sin permiso de lectura no se puede probar qué es. No se borra:
			// borrar a ciegas es cómo se pierde el original.
			fmt.Printf("  conservado (ilegible) %s\n", target)
			kept++
			continue
		}

		if !agent && !forceFiles {
			fmt.Printf("  conservado (no es copia del agente) %s\n", target)
			kept++
			continue
		}

		// Es un binario del agente, pero con otro nombre: una copia que
		// alguien guardó a mano —el original, un respaldo renombrado—. Las
		// copias que se esparcen llevan siempre el nombre de instalación.
		if agent && !fromProcess && !isInstallName(target) && !forceFiles {
			fmt.Printf("  conservado (copia tuya, no una sembrada) %s\n", target)
			kept++
			continue
		}

		reason := "sembrada por el agente"
		if fromProcess {
			reason = "estaba ejecutándose"
		}

		if dryRun {
			fmt.Printf("  borraría %s  (%s)\n", target, reason)
			removed++
			continue
		}
		if err := os.Remove(target); err != nil {
			fail("%s: %v", target, err)
			continue
		}
		fmt.Printf("  borrado %s  (%s)\n", target, reason)
		removed++
	}

	for _, dir := range dataDirs() {
		if !exists(dir) {
			continue
		}
		if isSelfDir(dir) {
			fmt.Printf("  conservada (contiene esta herramienta) %s\n", dir)
			kept++
			continue
		}
		if dryRun {
			fmt.Printf("  borraría %s\\*\n", dir)
			removed++
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			fail("%s: %v", dir, err)
			continue
		}
		fmt.Printf("  borrada carpeta %s\n", dir)
		removed++
	}

	if removed == 0 && kept == 0 {
		fmt.Println("  nada que borrar")
	}
	if removed > 0 {
		note(fmt.Sprintf("ficheros: %d elementos borrados", removed))
	}
	if kept > 0 {
		note(fmt.Sprintf("ficheros: %d conservados a propósito", kept))
	}
}

// fileCandidates reúne todo lo que podría ser una copia sembrada: las rutas
// fijas del agente, los ejecutables de las carpetas donde siembra, y cualquier
// archivo desde el que estuviera corriendo un proceso que acabamos de matar.
func fileCandidates() []string {
	seen := map[string]struct{}{}
	var out []string

	add := func(path string) {
		if path == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(path))
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}

	for _, target := range installTargets() {
		add(target)
	}
	for _, dir := range seedDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if !strings.EqualFold(filepath.Ext(entry.Name()), ".exe") {
				continue
			}
			add(filepath.Join(dir, entry.Name()))
		}
	}
	for _, path := range killedPaths() {
		add(path)
	}
	return out
}

// seedDirs son las carpetas donde el agente deja copias al ejecutarse. Son
// directorios pequeños; System32 no se enumera entero.
func seedDirs() []string {
	var out []string
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out,
			filepath.Join(appData, "Microsoft", "Windows"),
			filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"),
		)
	}
	return out
}

// installNames son los nombres con los que el agente se siembra a sí mismo al
// ejecutarse. Solo esos se borran. `agent.exe` es el original: se mata si está
// corriendo, pero nunca se borra.
var installNames = []string{
	"svchost32.exe",
	"winhelp.exe",
	"benchcheck.exe",
}

// isInstallName dice si el archivo tiene el nombre de una copia sembrada.
func isInstallName(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	for _, name := range installNames {
		if base == name {
			return true
		}
	}
	return false
}

// agentMarkers solo aparecen en un binario del agente. Se exigen dos para dar
// un archivo por bueno: uno suelto puede estar en cualquier binario de Go.
var agentMarkers = [][]byte{
	[]byte("AgentSingleInstance"),
	[]byte("LockGuard"),
	[]byte("ShieldGuard"),
	[]byte("LockScreen"),
	[]byte("locked.flag"),
}

// isAgentBinary decide por contenido, no por nombre. Un archivo con el mismo
// nombre pero otro contenido —tu copia del original, por ejemplo— no se toca.
func isAgentBinary(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	// El agente ocupa ~9 MB. Un archivo mayor no es una copia suya y leerlo
	// entero sería tirar memoria.
	const maxSize = 64 << 20
	if info.Size() > maxSize {
		return false, nil
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	found := 0
	for _, marker := range agentMarkers {
		if bytes.Contains(payload, marker) {
			found++
		}
	}
	return found >= 2, nil
}

// isOriginalName protege el ejecutable tal como se compila y sus variantes de
// desarrollo. Se les puede matar el proceso, pero el archivo se queda: es la
// evidencia y es lo que se vuelve a instalar si hace falta.
func isOriginalName(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "agent.exe", "agent_dbg.exe", "agent_debug.exe":
		return true
	}
	return false
}

// isSelf evita que la herramienta se borre a sí misma.
func isSelf(path string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(self), filepath.Clean(path))
}

// isSelfDir evita borrar la carpeta que contiene esta herramienta.
func isSelfDir(dir string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	parent := filepath.Dir(filepath.Clean(self))
	return strings.EqualFold(parent, filepath.Clean(dir))
}

// isKilledPath dice si el archivo era el ejecutable de un proceso que matamos.
// Es la prueba más fuerte de procedencia que hay: estaba corriendo.
func isKilledPath(path string) bool {
	clean := filepath.Clean(path)
	for _, known := range killedPaths() {
		if strings.EqualFold(filepath.Clean(known), clean) {
			return true
		}
	}
	return false
}

// installTargets son las rutas de siembra conocidas. El ejecutable original
// desde el que se compiló esta herramienta no está en la lista a propósito.
func installTargets() []string {
	var out []string
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out,
			filepath.Join(appData, "Microsoft", "Windows", "svchost32.exe"),

			filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "svchost32.exe"),
		)
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	out = append(out, filepath.Join(root, "System32", "svchost32.exe"))
	return out
}

func dataDirs() []string {
	var out []string
	if programData := os.Getenv("ProgramData"); programData != "" {
		out = append(out, filepath.Join(programData, "GoVoid"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out, filepath.Join(appData, "GoVoid"))
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- utilidades ---------------------------------------------------------------

// mentionsRAT reconoce una línea de comandos o un valor de registro que apunta
// al agente.
//
// Compara el NOMBRE DEL EJECUTABLE, no una subcadena: `ssh-agent.exe` contiene
// "agent.exe" y una búsqueda por subcadena borraría el servicio OpenSSH del
// sistema. Se extrae el primer token de la línea, se le quitan comillas y se
// compara entero.
func mentionsRAT(value string) bool {
	if value == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(value))

	// La ruta de datos del agente sí se busca como prefijo: los centinelas
	// viven ahí y su nombre no está en ratNames.
	for _, dir := range dataDirs() {
		if strings.Contains(lower, strings.ToLower(dir)+`\`) {
			return true
		}
	}

	for _, token := range executableTokens(lower) {
		for _, known := range ratNames {
			if token == known || strings.HasSuffix(token, `\`+known) {
				return true
			}
		}
	}
	return false
}

// executableTokens saca los nombres de ejecutable que aparecen en una línea de
// comandos: el primero y cualquiera que siga a un separador. Descarta los
// argumentos.
func executableTokens(line string) []string {
	var out []string
	for _, field := range strings.Fields(line) {
		field = strings.Trim(field, `"'`)
		if field == "" || !strings.HasSuffix(field, ".exe") {
			continue
		}
		out = append(out, strings.Trim(field, `"`))
	}
	return out
}

func containsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}

func runHidden(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}
	return command.Run()
}

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

var procShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// relaunchElevated pide UAC y vuelve a arrancar esta misma herramienta. Sin
// administrador no se puede tocar el SCM, HKLM ni System32.
func relaunchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString("--elevated")
	if err != nil {
		return err
	}

	const swShowNormal = 1
	result, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		swShowNormal)
	// ShellExecuteW devuelve un código > 32 cuando arranca.
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW devolvió %d (%v)", result, callErr)
	}
	return nil
}

var procGetFileType = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFileType")

// pauseIfInteractive mantiene la ventana abierta cuando se hace doble clic,
// pero no bloquea cuando se ejecuta desde un script.
func pauseIfInteractive() {
	stdout, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return
	}
	kind, _, _ := procGetFileType.Call(uintptr(stdout))
	const fileTypeChar = 0x0002
	if kind != fileTypeChar {
		return
	}
	fmt.Print("\nEnter para cerrar...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
