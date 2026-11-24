//go:build windows

package win32

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

var procImpersonateLoggedOnUser = advapi32.NewProc("ImpersonateLoggedOnUser")

var systemProcesses = []string{"winlogon.exe", "services.exe", "lsass.exe"}

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
