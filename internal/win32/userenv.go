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

// Bloque de entorno para un proceso que nace con el token de otro usuario.
//
// CreateProcessAsUser no hereda el entorno del usuario cuando lpEnvironment es
// nil: el hijo recibe el bloque del llamante, que en la tarea de arranque es
// SYSTEM. Con eso APPDATA apunta al perfil del sistema, InstallPath() resuelve
// mal y el agente se instala en el sitio equivocado.
//
// CreateEnvironmentBlock sería lo natural, pero el bloque que devuelve en esta
// máquina lo rechaza CreateProcessAsUser con ERROR_INVALID_PARAMETER —se
// comprobó probando cada variante por separado—. Así que el bloque se arma a
// mano: se parte del entorno actual, que ya trae SystemRoot, PATH y COMSPEC
// correctos, y se sustituyen todas las claves que dependen del usuario.

var procGetUserProfileDirectory = userenv.NewProc("GetUserProfileDirectoryW")

// buildUserEnvironment devuelve el bloque listo para CreateProcessAsUser: una
// secuencia de cadenas "CLAVE=valor" separadas por NUL y cerrada por un NUL más.
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
		// Windows ignora las entradas que empiezan por "=" (rutas de unidad
		// por usuario) y no aportan nada aquí.
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
	// Doble NUL de cierre.
	block = append(block, 0)
	return block, nil
}

// userProfileDir pide a userenv el perfil del dueño del token.
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
	// La llamada devuelve el tamaño en caracteres, sin contar el NUL final.
	if size > 0 && int(size) <= len(buffer) {
		buffer = buffer[:size]
	}
	return windows.UTF16ToString(buffer), nil
}

// tokenAccount resuelve el nombre de cuenta y el dominio del token.
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

// freeEnvironmentBlock existe para que el llamador pueda soltar el bloque con
// la misma forma con la que lo crea. El bloque vive en el heap de Go y lo libera
// el recolector; esto solo mantiene viva la referencia hasta después de usarlo.
func freeEnvironmentBlock(block []uint16) {
	runtime.KeepAlive(block)
}
