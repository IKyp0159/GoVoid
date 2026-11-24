//go:build windows

package win32

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetUserProfileDirectory = userenv.NewProc("GetUserProfileDirectoryW")

func buildUserEnvironment(token windows.Token) ([]uint16, error) {
	profile, err := userProfileDir(token)
	if err != nil {
		return nil, err
	}

	overrides := map[string]string{
		"USERPROFILE":  profile,
		"APPDATA":      filepath.Join(profile, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(profile, "AppData", "Local"),
		"TEMP":         filepath.Join(profile, "AppData", "Local", "Temp"),
		"TMP":          filepath.Join(profile, "AppData", "Local", "Temp"),
		"HOMEDRIVE":    filepath.VolumeName(profile),
		"HOMEPATH":     strings.TrimPrefix(profile, filepath.VolumeName(profile)),
	}

	if account, domain, lookupErr := tokenAccount(token); lookupErr == nil {
		overrides["USERNAME"] = account
		overrides["USERDOMAIN"] = domain
	}

	entries := make([]string, 0, 64)
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if name == "" {
			continue
		}
		if _, overridden := overrides[strings.ToUpper(name)]; overridden {
			continue
		}
		entries = append(entries, item)
	}

	for name, value := range overrides {
		entries = append(entries, name+"="+value)
	}
	sort.Strings(entries)

	block := make([]uint16, 0, 4096)
	for _, entry := range entries {
		encoded, convErr := windows.UTF16FromString(entry)
		if convErr != nil {
			continue
		}
		block = append(block, encoded...)
	}
	block = append(block, 0)
	return block, nil
}

func userProfileDir(token windows.Token) (string, error) {
	if err := procGetUserProfileDirectory.Find(); err != nil {
		return "", fmt.Errorf("win32: GetUserProfileDirectory: %w", err)
	}

	var size uint32
	_, _, _ = procGetUserProfileDirectory.Call(
		uintptr(token), 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return "", fmt.Errorf("win32: GetUserProfileDirectory no devolvió tamaño")
	}

	buffer := make([]uint16, size)
	result, _, callErr := procGetUserProfileDirectory.Call(
		uintptr(token),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)))
	if result == 0 {
		return "", fmt.Errorf("win32: GetUserProfileDirectory: %w", callErr)
	}
	if size > 0 && int(size) <= len(buffer) {
		buffer = buffer[:size]
	}
	return windows.UTF16ToString(buffer), nil
}

func tokenAccount(token windows.Token) (account, domain string, err error) {
	user, tokenErr := token.GetTokenUser()
	if tokenErr != nil {
		return "", "", tokenErr
	}

	account, domain, _, lookupErr := user.User.Sid.LookupAccount("")
	if lookupErr != nil {
		return "", "", lookupErr
	}
	return account, domain, nil
}

func freeEnvironmentBlock(block []uint16) {
	runtime.KeepAlive(block)
}
