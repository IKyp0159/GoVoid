//go:build windows

package win32

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procProcessIdToSessionId = kernel32.NewProc("ProcessIdToSessionId")

const createNoWindow = 0x08000000

func LaunchAsUser(exe string, args []string) (uint32, error) {
	token, err := shellToken()
	if err != nil {
		return 0, err
	}
	defer token.Close()

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
	var info windows.ProcessInformation

	err = windows.CreateProcessAsUser(token, exePtr, commandLinePtr, nil, nil,
		false, createNoWindow, nil, nil, &startup, &info)
	if err != nil {
		return 0, fmt.Errorf("win32: CreateProcessAsUser: %w", err)
	}

	_ = windows.CloseHandle(info.Thread)
	_ = windows.CloseHandle(info.Process)
	return info.ProcessId, nil
}

func shellToken() (windows.Token, error) {
	_ = enablePrivilege("SeImpersonatePrivilege")
	_ = enablePrivilege("SeAssignPrimaryTokenPrivilege")

	sessionID, err := currentSessionID()
	if err != nil {
		return 0, err
	}

	pids, err := pidsByName("explorer.exe")
	if err != nil {
		return 0, err
	}
	if len(pids) == 0 {
		return 0, errors.New("win32: explorer.exe no está corriendo")
	}

	var lastErr error
	for _, pid := range pids {
		if id, idErr := processSessionID(pid); idErr == nil && id != sessionID {
			continue
		}

		process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if openErr != nil {
			lastErr = openErr
			continue
		}

		var token windows.Token
		tokenErr := windows.OpenProcessToken(process,
			windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY, &token)
		_ = windows.CloseHandle(process)
		if tokenErr != nil {
			lastErr = tokenErr
			continue
		}

		var duplicated windows.Token
		dupErr := windows.DuplicateTokenEx(token, windows.TOKEN_ALL_ACCESS, nil,
			windows.SecurityImpersonation, windows.TokenPrimary, &duplicated)
		_ = token.Close()
		if dupErr != nil {
			lastErr = dupErr
			continue
		}
		return duplicated, nil
	}

	if lastErr == nil {
		lastErr = errors.New("win32: sin token de explorer.exe")
	}
	return 0, lastErr
}

func currentSessionID() (uint32, error) {
	return processSessionID(windows.GetCurrentProcessId())
}

func processSessionID(pid uint32) (uint32, error) {
	var session uint32
	result, _, callErr := procProcessIdToSessionId.Call(uintptr(pid), uintptr(unsafe.Pointer(&session)))
	if result == 0 {
		return 0, callErr
	}
	return session, nil
}

func buildCommandLine(exe string, args []string) string {
	line := quoteWindows(exe)
	for _, arg := range args {
		line += " " + quoteWindows(arg)
	}
	return line
}

func quoteWindows(value string) string {
	needsQuote := false
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '"' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return value
	}

	quoted := `"`
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '"':
			quoted += `\"`
		case '\\':
			j := i
			for j < len(value) && value[j] == '\\' {
				j++
			}
			if j == len(value) || value[j] == '"' {
				quoted += string(value[i:j]) + string(value[i:j])
			} else {
				quoted += string(value[i:j])
			}
			i = j - 1
		default:
			quoted += string(value[i])
		}
	}
	return quoted + `"`
}
