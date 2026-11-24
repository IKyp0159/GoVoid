//go:build windows

package win32

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessesByNameYTerminate(t *testing.T) {
	cmd := exec.Command("ping.exe", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("no se pudo lanzar el proceso de prueba: %v", err)
	}
	pid := uint32(cmd.Process.Pid)
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		for _, candidate := range processesByName([]string{"ping.exe"}) {
			if candidate == pid {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		t.Fatalf("processesByName no vio el PID %d", pid)
	}

	if !terminateAndWait(pid, 3*time.Second) {
		t.Fatalf("terminateAndWait no confirmó la salida del PID %d", pid)
	}

	time.Sleep(150 * time.Millisecond)
	for _, candidate := range processesByName([]string{"ping.exe"}) {
		if candidate == pid {
			t.Fatalf("el PID %d sigue vivo tras terminarlo", pid)
		}
	}
}

func TestProcessesByNameIgnoraDesconocidos(t *testing.T) {
	pids := processesByName([]string{"no-existe-jamas.exe"})
	if len(pids) != 0 {
		t.Fatalf("esperaba 0 procesos, llegaron %v", pids)
	}
}

func TestSQLiteExecPurgaGoogle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("no se pudo crear el archivo: %v", err)
	}

	if err := sqliteExec(path, `CREATE TABLE cookies(host_key TEXT, name TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if err := sqliteExec(path,
		`INSERT INTO cookies(host_key, name) VALUES
		 ('.google.com','SID'),
		 ('.youtube.com','SID'),
		 ('.google.com.mx','SID'),
		 ('.example.com','session'),
		 ('example.org','token')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := sqliteExec(path, googlePurgeSQL); err != nil {
		t.Fatalf("DELETE: %v", err)
	}

	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	tables, err := db.Tables()
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	table, ok := tables["cookies"]
	if !ok {
		t.Fatal("falta la tabla cookies")
	}
	rows, err := db.ReadTable(table.RootPage, 100)
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("esperaba 2 filas tras la purga, quedaron %d", len(rows))
	}
	hosts := map[string]bool{}
	for _, row := range rows {
		if len(row) > 0 {
			hosts[row[0].Text] = true
		}
	}
	if !hosts[".example.com"] || !hosts["example.org"] {
		t.Fatalf("la purga tocó dominios ajenos: %v", hosts)
	}
}

func TestSQLiteExecRechazaRutaInexistente(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-existe")
	if err := sqliteExec(path, googlePurgeSQL); err == nil {
		t.Fatal("esperaba error al abrir una base inexistente")
	}
}

func TestIsGoogleHost(t *testing.T) {
	casos := map[string]bool{
		"google.com":            true,
		".google.com":           true,
		"accounts.google.com":   true,
		"google.com.mx":         true,
		"www.google.co.uk":      true,
		"youtube.com":           true,
		"www.youtube.com":       true,
		"notgoogle.com":         false,
		"googleusercontent.com": false,
		"example.com":           false,
	}
	for host, esperado := range casos {
		if got := isGoogleHost(host); got != esperado {
			t.Fatalf("isGoogleHost(%q) = %v, esperaba %v", host, got, esperado)
		}
	}
}

func TestBrowserProcessNames(t *testing.T) {
	if names := browserProcessNames("Chrome"); len(names) != 1 || names[0] != "chrome.exe" {
		t.Fatalf("Chrome mal mapeado: %v", names)
	}
	if names := browserProcessNames("Brave-Browser"); len(names) != 1 || names[0] != "brave.exe" {
		t.Fatalf("Brave mal mapeado: %v", names)
	}
	if names := browserProcessNames("desconocido"); len(names) != 0 {
		t.Fatalf("esperaba sin procesos: %v", names)
	}
}
