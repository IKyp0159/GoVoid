//go:build windows

package win32

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type GoogleProfile struct {
	Name          string
	Browser       string
	Account       string
	Dir           string
	Cookies       string
	LocalState    string
	GoogleCookies int
	Session       bool
	Running       bool
}

type GoogleCloseResult struct {
	Profile       string
	Browser       string
	Account       string
	BeforeCookies int
	AfterCookies  int
	Session       bool
	Purged        bool
	AccountClosed bool
	Err           string
}

type GoogleCloseReport struct {
	Results   []GoogleCloseResult
	Found     int
	Killed    int
	Remaining int
	Elapsed   time.Duration
}

var googleSessionCookies = map[string]bool{
	"sid":               true,
	"ssid":              true,
	"hsid":              true,
	"apisid":            true,
	"sapisid":           true,
	"lsid":              true,
	"osid":              true,
	"__secure-1psid":    true,
	"__secure-3psid":    true,
	"__secure-1pasid":   true,
	"__secure-3pasid":   true,
	"__secure-1psidts":  true,
	"__secure-3psidts":  true,
	"__secure-1psidcc":  true,
	"__secure-3psidcc":  true,
	"__secure-1psidrts": true,
	"__secure-3psidrts": true,
	"__host-1plsid":     true,
	"__host-3plsid":     true,
	"__host-gaps":       true,
	"__secure-1papisid": true,
}

const googlePurgeSQL = `DELETE FROM cookies WHERE host_key='google.com' OR host_key LIKE '%.google.com' OR host_key LIKE '%.google.%' OR host_key LIKE 'google.%' OR host_key='youtube.com' OR host_key LIKE '%.youtube.com'`

func GoogleProfiles() []GoogleProfile {
	accounts := chromeAccounts()
	running := runningBrowsers()
	profiles := ChromeProfiles()

	out := make([]GoogleProfile, 0, len(profiles))
	for _, profile := range profiles {
		item := GoogleProfile{
			Name:       profile.Name,
			Browser:    profile.Browser,
			Dir:        profile.Dir,
			Cookies:    profile.Cookies,
			LocalState: profile.LocalState,
		}
		item.Account = accounts[profile.Dir]
		item.GoogleCookies, item.Session = googleCookieStats(profile.Cookies)
		item.Running = running[profile.Browser]
		out = append(out, item)
	}
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
	})
	return out
}

func CloseGoogleSessions(targets []string) GoogleCloseReport {
	started := time.Now()
	all := GoogleProfiles()

	selected := all
	if len(targets) > 0 {
		wanted := make(map[string]bool, len(targets))
		for _, target := range targets {
			wanted[target] = true
		}
		selected = make([]GoogleProfile, 0, len(targets))
		for _, profile := range all {
			if wanted[profile.Name] {
				selected = append(selected, profile)
			}
		}
	}

	report := GoogleCloseReport{Found: len(selected)}
	if len(selected) == 0 {
		report.Elapsed = time.Since(started)
		return report
	}

	browsers := make(map[string]bool, 3)
	for _, profile := range selected {
		browsers[profile.Browser] = true
	}

	killed := make(map[uint32]bool, 32)
	for attempt := 0; attempt < 4; attempt++ {
		found := false
		for browser := range browsers {
			for _, pid := range processesByName(browserProcessNames(browser)) {
				if killed[pid] {
					continue
				}
				found = true
				if terminateAndWait(pid, 2*time.Second) {
					killed[pid] = true
				}
			}
		}
		if !found {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	report.Killed = len(killed)

	for _, profile := range selected {
		result := GoogleCloseResult{
			Profile:       profile.Name,
			Browser:       profile.Browser,
			Account:       profile.Account,
			BeforeCookies: profile.GoogleCookies,
			Session:       profile.Session,
		}
		if err := purgeGoogleCookies(profile.Cookies); err != nil {
			result.Err = err.Error()
		} else {
			result.Purged = true
		}
		if err := closeChromeAccount(profile.Dir, profile.LocalState); err != nil {
			if result.Err != "" {
				result.Err += "; "
			}
			result.Err += err.Error()
		} else {
			result.AccountClosed = true
		}
		result.AfterCookies, _ = googleCookieStats(profile.Cookies)
		report.Results = append(report.Results, result)
	}

	for browser := range browsers {
		report.Remaining += len(processesByName(browserProcessNames(browser)))
	}
	report.Elapsed = time.Since(started)
	return report
}

func purgeGoogleCookies(cookiesPath string) error {
	if cookiesPath == "" {
		return errors.New("el perfil no tiene base de cookies")
	}
	if info, err := os.Stat(cookiesPath); err != nil || info.IsDir() {
		return errors.New("la base de cookies no existe")
	}
	return sqliteExec(cookiesPath, googlePurgeSQL)
}

var chromeAccountPrefs = []string{
	"account_info",
	"account_values",
	"account_tracker_service_last_update",
	"signin",
	"sync",
	"google",
}

func closeChromeAccount(profileDir, localStatePath string) error {
	var errs []error

	if profileDir != "" {
		prefs := filepath.Join(profileDir, "Preferences")
		if err := stripJSONKeys(prefs, chromeAccountPrefs); err != nil {
			errs = append(errs, fmt.Errorf("Preferences: %w", err))
		}
		for _, name := range []string{"Device Bound Sessions", "Device Bound Sessions-journal"} {
			path := filepath.Join(profileDir, "Network", name)
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}

	if localStatePath != "" {
		if err := stripJSONKeys(localStatePath, []string{"account_info", "signin"}); err != nil {
			errs = append(errs, fmt.Errorf("Local State: %w", err))
		}
	}

	return errors.Join(errs...)
}

func stripJSONKeys(path string, keys []string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("JSON inválido: %w", err)
	}

	changed := false
	for _, key := range keys {
		if _, ok := document[key]; ok {
			delete(document, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}

	updated, err := json.Marshal(document)
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".govoid_*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(updated); err != nil {
		temp.Close()
		_ = os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}

func googleCookieStats(cookiesPath string) (int, bool) {
	if cookiesPath == "" {
		return 0, false
	}

	copied, cleanup, err := CopyBrowserDB(cookiesPath)
	if err != nil {
		return 0, false
	}
	defer cleanup()

	db, err := OpenSQLite(copied)
	if err != nil {
		return 0, false
	}
	tables, err := db.Tables()
	if err != nil {
		return 0, false
	}
	table, ok := tables["cookies"]
	if !ok {
		return 0, false
	}

	columns := ColumnNames(table.SQL)
	hostIndex := IndexOfColumn(columns, "host_key")
	nameIndex := IndexOfColumn(columns, "name")
	if hostIndex < 0 || nameIndex < 0 {
		return 0, false
	}

	rows, err := db.ReadTable(table.RootPage, 200000)
	if err != nil {
		return 0, false
	}

	width := hostIndex
	if nameIndex > width {
		width = nameIndex
	}

	count := 0
	session := false
	for _, row := range rows {
		if len(row) <= width {
			continue
		}
		host := strings.ToLower(strings.TrimPrefix(row[hostIndex].Text, "."))
		if !isGoogleHost(host) {
			continue
		}
		count++
		if googleSessionCookies[strings.ToLower(row[nameIndex].Text)] {
			session = true
		}
	}
	return count, session
}

func isGoogleHost(host string) bool {
	if host == "google.com" || strings.HasSuffix(host, ".google.com") {
		return true
	}
	if host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") {
		return true
	}
	if strings.HasPrefix(host, "google.") || strings.Contains(host, ".google.") {
		return true
	}
	return false
}

func runningBrowsers() map[string]bool {
	out := make(map[string]bool, 3)
	for _, browser := range []string{"Chrome", "Edge", "Brave-Browser"} {
		out[browser] = len(processesByName(browserProcessNames(browser))) > 0
	}
	return out
}

func browserProcessNames(browser string) []string {
	switch strings.ToLower(browser) {
	case "chrome":
		return []string{"chrome.exe"}
	case "edge":
		return []string{"msedge.exe"}
	case "brave-browser":
		return []string{"brave.exe"}
	default:
		return nil
	}
}

func processesByName(names []string) []uint32 {
	if len(names) == 0 {
		return nil
	}

	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = true
	}

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	self := uint32(os.Getpid())
	pids := make([]uint32, 0, 16)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return pids
	}
	for {
		pid := entry.ProcessID
		if pid != 0 && pid != self && wanted[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))] {
			pids = append(pids, pid)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return pids
}

func terminateAndWait(pid uint32, timeout time.Duration) bool {
	handle, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	if err := windows.TerminateProcess(handle, 1); err != nil {
		return false
	}
	millis := uint32(timeout / time.Millisecond)
	event, waitErr := windows.WaitForSingleObject(handle, millis)
	if waitErr != nil {
		return false
	}
	return event == windows.WAIT_OBJECT_0
}

type chromeLocalState struct {
	Profile struct {
		InfoCache map[string]struct {
			UserName string `json:"user_name"`
			GAIAName string `json:"gaia_name"`
		} `json:"info_cache"`
	} `json:"profile"`
}

func chromeAccounts() map[string]string {
	out := make(map[string]string, 8)
	for _, userData := range chromUserDataDirs() {
		raw, err := os.ReadFile(filepath.Join(userData, "Local State"))
		if err != nil {
			continue
		}
		var state chromeLocalState
		if err := json.Unmarshal(raw, &state); err != nil {
			continue
		}
		for dirName, info := range state.Profile.InfoCache {
			account := strings.TrimSpace(info.GAIAName)
			if account == "" {
				account = strings.TrimSpace(info.UserName)
			}
			if account == "" {
				continue
			}
			out[filepath.Join(userData, dirName)] = account
		}
	}
	return out
}

func GoogleSummary(profiles []GoogleProfile) (sessions, running int) {
	for _, profile := range profiles {
		if profile.Session {
			sessions++
		}
		if profile.Running {
			running++
		}
	}
	return sessions, running
}

func GoogleProfileNames(profiles []GoogleProfile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.Name)
	}
	return names
}

func FormatGoogleAccount(account string) string {
	account = strings.TrimSpace(account)
	if account == "" {
		return "sin cuenta"
	}
	return account
}

func DescribeGoogleProfile(profile GoogleProfile, index int) string {
	state := "sin sesión"
	if profile.Session {
		state = "sesión activa"
	}
	open := "cerrado"
	if profile.Running {
		open = "abierto"
	}
	return fmt.Sprintf("%d. %s · %s · %d cookies Google · %s · %s",
		index, profile.Name, FormatGoogleAccount(profile.Account), profile.GoogleCookies, state, open)
}
