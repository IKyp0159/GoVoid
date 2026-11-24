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

var (
	shadowDevicePattern = regexp.MustCompile(`\\\\\?\\GLOBALROOT\\Device\\HarddiskVolumeShadowCopy\d+`)
	shadowIDPattern     = regexp.MustCompile(`\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}`)
)

type ShadowCopy struct {
	Device  string
	id      string
	cleanup func()
}

func (s *ShadowCopy) Close() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
		s.cleanup = nil
	}
}

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

const createShadowScript = `$ErrorActionPreference='Stop'
$cls=Get-WmiObject -List Win32_ShadowCopy
$res=$cls.Create('%s','ClientAccessible')
if($res.ReturnValue -ne 0){ Write-Output ('ERR '+$res.ReturnValue); exit 1 }
$obj=Get-WmiObject Win32_ShadowCopy | Where-Object { $_.ID -eq $res.ShadowID }
Write-Output $res.ShadowID
Write-Output $obj.DeviceObject`

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

func deleteShadowCopy(id string) error {
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$obj=Get-WmiObject Win32_ShadowCopy | Where-Object { $_.ID -eq '%s' }
if($obj){ $null=$obj.Delete() }`, id)
	_, err := runPowerShell(script)
	return err
}

func runPowerShell(script string) (string, error) {
	return runHiddenOutput("powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", script)
}

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
