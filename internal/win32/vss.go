//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Chrome mantiene Network\Cookies abierto sin FILE_SHARE_READ, así que mientras
// el navegador corre el archivo no se puede leer ni copiar: CreateFile devuelve
// ERROR_SHARING_VIOLATION y ninguna bandera lo salta. La única vía es leerlo
// desde una copia de sombra del volumen, que es un snapshot coherente y no pasa
// por el modo de compartición del original.
//
// vssadmin en Windows 11 ya no admite `create shadow`, así que el snapshot se
// crea por WMI (Win32_ShadowCopy). La salida se parsea por forma, no por texto:
// la ruta del snapshot siempre es \\?\GLOBALROOT\Device\
// HarddiskVolumeShadowCopyN y el identificador siempre es un GUID entre llaves,
// así que el idioma del sistema da igual.

var (
	shadowDevicePattern = regexp.MustCompile(`\\\\\?\\GLOBALROOT\\Device\\HarddiskVolumeShadowCopy\d+`)
	shadowIDPattern     = regexp.MustCompile(`\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}`)
)

// ShadowCopy es un snapshot montado y la función que lo elimina.
type ShadowCopy struct {
	Device  string
	id      string
	cleanup func()
}

// Close elimina la copia de sombra. Es obligatorio: cada snapshot retiene
// espacio del volumen hasta que se borra.
func (s *ShadowCopy) Close() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
		s.cleanup = nil
	}
}

// PathFor traduce una ruta normal a la misma ruta dentro del snapshot.
func (s *ShadowCopy) PathFor(path string) (string, error) {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return "", errors.New("vss: la ruta no tiene unidad")
	}
	rest := strings.TrimPrefix(path, volume)
	if !strings.HasPrefix(rest, `\`) {
		return "", errors.New("vss: ruta absoluta esperada")
	}
	return s.Device + rest, nil
}

// createShadowScript crea el snapshot y devuelve el GUID y la ruta del montaje.
// Se usan dos líneas separadas para poder separar los dos valores.
const createShadowScript = `$ErrorActionPreference='Stop'
$cls=Get-WmiObject -List Win32_ShadowCopy
$res=$cls.Create('%s','ClientAccessible')
if($res.ReturnValue -ne 0){ Write-Output ('ERR '+$res.ReturnValue); exit 1 }
$obj=Get-WmiObject Win32_ShadowCopy | Where-Object { $_.ID -eq $res.ShadowID }
Write-Output $res.ShadowID
Write-Output $obj.DeviceObject`

// CreateShadowCopy crea un snapshot del volumen que contiene path.
func CreateShadowCopy(path string) (*ShadowCopy, error) {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return nil, errors.New("vss: la ruta no tiene unidad")
	}
	root := volume + `\`

	script := fmt.Sprintf(createShadowScript, root)
	output, err := runPowerShell(script)
	if err != nil {
		return nil, fmt.Errorf("vss: crear snapshot: %w (%s)", err, firstLine(output))
	}

	device := shadowDevicePattern.FindString(output)
	if device == "" {
		return nil, fmt.Errorf("vss: sin ruta de snapshot: %s", firstLine(output))
	}

	shadow := &ShadowCopy{Device: device}
	if id := shadowIDPattern.FindString(output); id != "" {
		shadow.id = id
		shadow.cleanup = func() { _ = deleteShadowCopy(id) }
	}
	return shadow, nil
}

// deleteShadowCopy borra el snapshot por GUID. Si falla, el snapshot queda
// montado pero el archivo ya se copió.
func deleteShadowCopy(id string) error {
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$obj=Get-WmiObject Win32_ShadowCopy | Where-Object { $_.ID -eq '%s' }
if($obj){ $null=$obj.Delete() }`, id)
	_, err := runPowerShell(script)
	return err
}

// runPowerShell ejecuta un script sin consola visible y devuelve su salida.
func runPowerShell(script string) (string, error) {
	return runHiddenOutput("powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", script)
}

// runHiddenOutput ejecuta un comando sin consola visible y devuelve stdout más
// stderr, que es de donde sale la ruta del snapshot.
func runHiddenOutput(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		return text[:index]
	}
	return text
}
