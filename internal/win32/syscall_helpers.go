//go:build windows

package win32

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Bibliotecas del sistema cargadas de forma perezosa. Cero CGO.
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	advapi32 = windows.NewLazySystemDLL("advapi32.dll")
	ntdll    = windows.NewLazySystemDLL("ntdll.dll")
	crypt32  = windows.NewLazySystemDLL("crypt32.dll")
	wtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")
	userenv  = windows.NewLazySystemDLL("userenv.dll")
)

// uintptrToPointer convierte un uintptr devuelto por la API Win32 en puntero.
// El doble cast evita el falso positivo de go vet sobre uintptr → unsafe.Pointer.
func uintptrToPointer(value uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&value))
}
