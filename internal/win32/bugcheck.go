//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/govoid/internal/config"
)

const pendingBugcheckName = "bsod_pending"

func pendingBugcheckPath() string {
	if programData := os.Getenv("ProgramData"); programData != "" {
		return filepath.Join(programData, config.DataDirName, pendingBugcheckName)
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, config.DataDirName, pendingBugcheckName)
}

func MarkPendingBugcheck(method string) error {
	path := pendingBugcheckPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339), method)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func PendingBugcheck() (string, bool) {
	raw, err := os.ReadFile(pendingBugcheckPath())
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", false
	}
	return text, true
}

func ClearPendingBugcheck() error {
	err := os.Remove(pendingBugcheckPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func LastBugcheckCode() (code string, when time.Time, err error) {
	script := `$out = 'none'
$e = Get-WinEvent -FilterHashtable @{LogName='System';Id=1001} -MaxEvents 1 -ErrorAction SilentlyContinue
if ($e) { $out = '{0}|{1}' -f $e.Properties[0].Value, $e.TimeCreated.ToUniversalTime().ToString('o') }
else {
  $p = Get-WinEvent -FilterHashtable @{LogName='System';Id=41} -MaxEvents 1 -ErrorAction SilentlyContinue
  if ($p) {
    $x = [xml]$p.ToXml()
    $d = $x.Event.EventData.Data | Where-Object { $_.Name -eq 'BugcheckCode' }
    if ($d -and [int]$d.'#text' -ne 0) { $out = '0x{0:x8}|{1}' -f [int]$d.'#text', $p.TimeCreated.ToUniversalTime().ToString('o') }
  }
}
if ($out -eq 'none') {
  $m = Get-ChildItem (Join-Path $env:SystemRoot 'Minidump') -Filter *.dmp -ErrorAction SilentlyContinue |
       Sort-Object LastWriteTime -Descending | Select-Object -First 1
  if ($m) { $out = 'desconocido|{0}' -f $m.LastWriteTime.ToUniversalTime().ToString('o') }
}
$out`

	out, callErr := runPowerShell(script)
	text := strings.TrimSpace(out)
	if text == "" || text == "none" {
		if callErr != nil {
			return "", time.Time{}, callErr
		}
		return "", time.Time{}, errors.New("sin evento de bugcheck en el registro")
	}
	if index := strings.LastIndexByte(text, '\n'); index >= 0 {
		text = strings.TrimSpace(text[index+1:])
	}

	rawCode, rawTime, found := strings.Cut(text, "|")
	if !found {
		return strings.TrimSpace(text), time.Time{}, nil
	}
	stamp, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(rawTime))
	if parseErr != nil {
		return strings.TrimSpace(rawCode), time.Time{}, nil
	}
	return strings.TrimSpace(rawCode), stamp, nil
}
