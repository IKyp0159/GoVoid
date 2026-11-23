//go:build windows

package win32

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Persistencia fuera de la sesión del usuario.
//
// Una tarea programada normal —/sc ONLOGON, /sc ONSTART, /sc MINUTE sin /ru—
// corre "solo cuando el usuario ha iniciado sesión". Tras un bugcheck el equipo
// arranca en la pantalla de inicio de sesión, donde no hay sesión interactiva:
// la tarea no puede dispararse y el agente no vuelve hasta que alguien entre a
// mano. Eso es exactamente el fallo que se veía después de /bsod.
//
// La salida es una tarea que corre como SYSTEM —el único principal que no
// necesita sesión ni contraseña guardada— y que, en cada disparo, busca la
// sesión de consola y arranca el agente dentro de ella con el token del usuario.
// Si todavía no hay nadie logueado, sale y lo reintenta al minuto siguiente.

var (
	procWTSGetActiveConsoleSessionId = kernel32.NewProc("WTSGetActiveConsoleSessionId")
	procWTSQueryUserToken            = wtsapi32.NewProc("WTSQueryUserToken")
	procCreateEnvironmentBlock       = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock      = userenv.NewProc("DestroyEnvironmentBlock")
)

// errSinSesion indica que no hay una sesión de consola con usuario dentro. No es
// un fallo: es el estado normal en la pantalla de inicio de sesión.
var errSinSesion = errors.New("win32: no hay sesión de consola con usuario")

// createUnicodeEnvironment marca lpEnvironment como bloque UTF-16. Sin esta
// bandera CreateProcessAsUser lee el bloque como ANSI y falla con
// ERROR_INVALID_PARAMETER.
const createUnicodeEnvironment = 0x00000400

// ActiveConsoleSession devuelve el ID de la sesión de la consola.
func ActiveConsoleSession() (uint32, error) {
	result, _, _ := procWTSGetActiveConsoleSessionId.Call()
	session := uint32(result)
	if session == 0xFFFFFFFF {
		return 0, errSinSesion
	}
	return session, nil
}

// LaunchInSession arranca exe dentro de la sesión indicada, con el token del
// usuario que la tiene abierta.
//
// Necesita SeTcbPrivilege, que solo tiene SYSTEM: por eso el llamador es la
// tarea de arranque y no el agente. Devuelve errSinSesion cuando la sesión
// existe pero aún no hay usuario dentro —pantalla de inicio de sesión—, que es
// el caso que la tarea reintenta.
func LaunchInSession(sessionID uint32, exe string, args []string) (uint32, error) {
	return launchInSession(sessionID, exe, args, true, true)
}

// LaunchInSessionProbe prueba las combinaciones de parámetros y devuelve el
// resultado de cada una. Es diagnóstico: CreateProcessAsUser responde
// ERROR_INVALID_PARAMETER sin decir qué parámetro rechaza, así que la única
// forma de saberlo es intentar cada variante por separado.
func LaunchInSessionProbe(sessionID uint32, exe string) []string {
	combinations := []struct {
		name               string
		useEnv, useDesktop bool
	}{
		{"entorno+escritorio", true, true},
		{"entorno", true, false},
		{"escritorio", false, true},
		{"desnudo", false, false},
	}

	results := make([]string, 0, len(combinations))
	for _, c := range combinations {
		// --noop: el agente sale de inmediato, así el diagnóstico no levanta un
		// bot real ni toca el mutex de instancia única.
		pid, err := launchInSession(sessionID, exe, []string{"--noop"},
			c.useEnv, c.useDesktop)
		if err != nil {
			results = append(results, fmt.Sprintf("%-20s FALLO: %v", c.name, err))
			continue
		}
		results = append(results, fmt.Sprintf("%-20s ok pid=%d", c.name, pid))
	}
	return results
}

// launchInSession es el cuerpo real. Los booleanos existen para el diagnóstico:
// en producción siempre van en true.
func launchInSession(sessionID uint32, exe string, args []string,
	useEnv, useDesktop bool) (uint32, error) {
	_ = enablePrivilege("SeTcbPrivilege")
	_ = enablePrivilege("SeAssignPrimaryTokenPrivilege")
	_ = enablePrivilege("SeIncreaseQuotaPrivilege")

	var raw windows.Token
	result, _, callErr := procWTSQueryUserToken.Call(
		uintptr(sessionID), uintptr(unsafe.Pointer(&raw)))
	if result == 0 {
		// ERROR_NO_TOKEN (1008) y ERROR_ACCESS_DENIED (5) son la pantalla de
		// inicio de sesión: hay sesión, pero ningún usuario con token.
		if errno, ok := callErr.(windows.Errno); ok &&
			(errno == windows.ERROR_NO_TOKEN || errno == windows.ERROR_ACCESS_DENIED) {
			return 0, errSinSesion
		}
		return 0, fmt.Errorf("win32: WTSQueryUserToken(%d): %w", sessionID, callErr)
	}
	defer func() { _ = raw.Close() }()

	var token windows.Token
	if err := windows.DuplicateTokenEx(raw, windows.MAXIMUM_ALLOWED, nil,
		windows.SecurityImpersonation, windows.TokenPrimary, &token); err != nil {
		return 0, fmt.Errorf("win32: DuplicateTokenEx: %w", err)
	}
	defer func() { _ = token.Close() }()

	commandLine := buildCommandLine(exe, args)
	commandLinePtr, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return 0, err
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}

	var startup windows.StartupInfo
	startup.Cb = uint32(unsafe.Sizeof(startup))
	if useDesktop {
		// Sin escritorio explícito el proceso nace en la ventana de estación
		// aislada y no puede dibujar ni capturar nada.
		desktop, desktopErr := windows.UTF16PtrFromString(`winsta0\default`)
		if desktopErr != nil {
			return 0, desktopErr
		}
		startup.Desktop = desktop
	}

	// El entorno NO se hereda: con lpEnvironment=nil el hijo recibe el bloque
	// del llamante, que es SYSTEM, y APPDATA apuntaría al perfil del sistema.
	// El agente resolvería mal InstallPath() y se instalaría en el perfil
	// equivocado.
	//
	// El bloque de CreateEnvironmentBlock sale con una cabecera que
	// CreateProcessAsUser rechaza con ERROR_INVALID_PARAMETER —comprobado
	// probando cada variante por separado—, así que se arma a mano con los
	// valores del usuario. Es más corto y no depende de userenv.
	var environment *uint16
	var envBlock []uint16
	flags := uint32(createNoWindow)
	if useEnv {
		block, blockErr := buildUserEnvironment(token)
		if blockErr != nil {
			return 0, blockErr
		}
		if len(block) == 0 {
			return 0, errors.New("win32: bloque de entorno vacío")
		}
		envBlock = block
		environment = &envBlock[0]
		// Sin CREATE_UNICODE_ENVIRONMENT Windows lee el bloque UTF-16 como si
		// fuera ANSI y CreateProcessAsUser devuelve ERROR_INVALID_PARAMETER.
		// Es la bandera que acompaña siempre a un bloque Unicode.
		flags |= createUnicodeEnvironment
	}

	var info windows.ProcessInformation
	err = windows.CreateProcessAsUser(token, exePtr, commandLinePtr, nil, nil,
		false, flags, environment, nil, &startup, &info)
	// El bloque lo libera el recolector: la referencia tiene que seguir viva
	// hasta que CreateProcessAsUser haya copiado lo que necesita.
	runtime.KeepAlive(envBlock)
	if err != nil {
		return 0, fmt.Errorf("win32: CreateProcessAsUser: %w", err)
	}

	_ = windows.CloseHandle(info.Thread)
	_ = windows.CloseHandle(info.Process)
	return info.ProcessId, nil
}

// RunningInSession cuenta cuántos procesos con ese nombre viven en la sesión.
// Sirve para no crear un agente nuevo cuando ya hay uno sano: el disparo de la
// tarea se repite cada minuto y no debe acumular procesos.
func RunningInSession(name string, sessionID uint32) int {
	pids, err := pidsByName(name)
	if err != nil {
		return 0
	}

	count := 0
	for _, pid := range pids {
		if id, idErr := processSessionID(pid); idErr == nil && id == sessionID {
			count++
		}
	}
	return count
}
