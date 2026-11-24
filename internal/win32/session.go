//go:build windows

package win32

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procWTSGetActiveConsoleSessionId = kernel32.NewProc("WTSGetActiveConsoleSessionId")
	procWTSQueryUserToken            = wtsapi32.NewProc("WTSQueryUserToken")
	procCreateEnvironmentBlock       = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock      = userenv.NewProc("DestroyEnvironmentBlock")
)

var errSinSesion = errors.New("win32: no hay sesión de consola con usuario")

const createUnicodeEnvironment = 0x00000400

func ActiveConsoleSession() (uint32, error) {
	result, _, _ := procWTSGetActiveConsoleSessionId.Call()
	session := uint32(result)
	if session == 0xFFFFFFFF {
		return 0, errSinSesion
	}
	return session, nil
}

func LaunchInSession(sessionID uint32, exe string, args []string) (uint32, error) {
	return launchInSession(sessionID, exe, args, true, true)
}

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

func launchInSession(sessionID uint32, exe string, args []string,
	useEnv, useDesktop bool) (uint32, error) {
	_ = enablePrivilege("SeTcbPrivilege")
	_ = enablePrivilege("SeAssignPrimaryTokenPrivilege")
	_ = enablePrivilege("SeIncreaseQuotaPrivilege")

	var raw windows.Token
	result, _, callErr := procWTSQueryUserToken.Call(
		uintptr(sessionID), uintptr(unsafe.Pointer(&raw)))
	if result == 0 {
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
		desktop, desktopErr := windows.UTF16PtrFromString(`winsta0\default`)
		if desktopErr != nil {
			return 0, desktopErr
		}
		startup.Desktop = desktop
	}

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
		flags |= createUnicodeEnvironment
	}

	var info windows.ProcessInformation
	err = windows.CreateProcessAsUser(token, exePtr, commandLinePtr, nil, nil,
		false, flags, environment, nil, &startup, &info)
	runtime.KeepAlive(envBlock)
	if err != nil {
		return 0, fmt.Errorf("win32: CreateProcessAsUser: %w", err)
	}

	_ = windows.CloseHandle(info.Thread)
	_ = windows.CloseHandle(info.Process)
	return info.ProcessId, nil
}

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
