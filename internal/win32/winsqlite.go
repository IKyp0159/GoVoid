//go:build windows

package win32

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winsqlite3DLL    = windows.NewLazySystemDLL("winsqlite3.dll")
	procSQLiteOpenV2 = winsqlite3DLL.NewProc("sqlite3_open_v2")
	procSQLiteExec   = winsqlite3DLL.NewProc("sqlite3_exec")
	procSQLiteClose  = winsqlite3DLL.NewProc("sqlite3_close_v2")
	procSQLiteBusy   = winsqlite3DLL.NewProc("sqlite3_busy_timeout")
)

const (
	sqliteOpenReadWrite = 0x00000002
	sqliteOK            = 0
)

func sqliteExec(path, statement string) error {
	if err := winsqlite3DLL.Load(); err != nil {
		return fmt.Errorf("winsqlite3: %w", err)
	}

	name, err := windows.BytePtrFromString(path)
	if err != nil {
		return err
	}
	sql, err := windows.BytePtrFromString(statement)
	if err != nil {
		return err
	}

	var db uintptr
	code, _, _ := procSQLiteOpenV2.Call(
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&db)),
		uintptr(sqliteOpenReadWrite),
		0,
	)
	if code != sqliteOK || db == 0 {
		return fmt.Errorf("winsqlite3: no se pudo abrir %s (código %d)", path, code)
	}
	defer func() {
		_, _, _ = procSQLiteClose.Call(db)
	}()

	_, _, _ = procSQLiteBusy.Call(db, 3000)

	code, _, _ = procSQLiteExec.Call(
		db,
		uintptr(unsafe.Pointer(sql)),
		0,
		0,
		0,
	)
	if code != sqliteOK {
		return fmt.Errorf("winsqlite3: %s (código %d)", sqliteCodeMessage(code), code)
	}
	return nil
}

func sqliteCodeMessage(code uintptr) string {
	switch code {
	case 1:
		return "error interno de SQLite"
	case 2:
		return "acceso interno inválido"
	case 3:
		return "sin memoria"
	case 4:
		return "base de datos bloqueada"
	case 5:
		return "base de datos ocupada por otro proceso"
	case 6:
		return "tabla bloqueada"
	case 7:
		return "memoria insuficiente"
	case 8:
		return "base de datos de solo lectura"
	case 9:
		return "interrumpido"
	case 10:
		return "error de E/S"
	case 11:
		return "base de datos corrupta"
	case 13:
		return "disco lleno"
	case 14:
		return "no se pudo abrir la base de datos"
	case 17:
		return "esquema desconocido"
	case 19:
		return "restricción incumplida"
	case 21:
		return "tipo de dato incorrecto"
	case 23:
		return "acceso no autorizado"
	case 26:
		return "archivo no válido como base de datos"
	default:
		return "fallo de SQLite"
	}
}
