//go:build windows

package win32

import (
	"errors"
	"fmt"
	"unsafe"
)

// dataBlob replica DATA_BLOB de dpapi.h.
type dataBlob struct {
	Size uint32
	Data *byte
}

var (
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
)

func blobFromBytes(data []byte) dataBlob {
	if len(data) == 0 {
		return dataBlob{}
	}
	return dataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func bytesFromBlob(blob dataBlob) []byte {
	if blob.Size == 0 || blob.Data == nil {
		return nil
	}
	out := make([]byte, blob.Size)
	copy(out, unsafe.Slice(blob.Data, blob.Size))
	return out
}

// DPAPIUnprotect descifra un blob protegido con CryptProtectData en el mismo
// usuario y máquina.
func DPAPIUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("dpapi: blob vacío")
	}

	in := blobFromBytes(data)
	var out dataBlob

	res, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // descripción de salida
		0, // entropía adicional
		0, // reservado
		0, // prompt
		0, // flags
		uintptr(unsafe.Pointer(&out)),
	)
	if res == 0 {
		return nil, fmt.Errorf("dpapi: CryptUnprotectData falló: %v", callErr)
	}
	defer localFree(uintptr(unsafe.Pointer(out.Data)))

	plain := bytesFromBlob(out)
	if len(plain) == 0 {
		return nil, errors.New("dpapi: salida vacía")
	}
	return plain, nil
}

// DPAPIUnprotectSystem descifra un blob protegido a nivel SYSTEM. Solo
// funciona corriendo como SYSTEM o con SeTcbPrivilege; lo necesita la clave
// app-bound de Chrome v20.
func DPAPIUnprotectSystem(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("dpapi: blob vacío")
	}

	const cryptprotectSystem = 0x1

	in := blobFromBytes(data)
	var out dataBlob

	res, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		cryptprotectSystem,
		uintptr(unsafe.Pointer(&out)),
	)
	if res == 0 {
		return nil, fmt.Errorf("dpapi: CryptUnprotectData(SYSTEM) falló: %v", callErr)
	}
	defer localFree(uintptr(unsafe.Pointer(out.Data)))

	plain := bytesFromBlob(out)
	if len(plain) == 0 {
		return nil, errors.New("dpapi: salida SYSTEM vacía")
	}
	return plain, nil
}

// DPAPIProtect cifra datos para el usuario actual. Lo usa el autotest.
func DPAPIProtect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("dpapi: datos vacíos")
	}

	in := blobFromBytes(data)
	var out dataBlob

	res, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&out)),
	)
	if res == 0 {
		return nil, fmt.Errorf("dpapi: CryptProtectData falló: %v", callErr)
	}
	defer localFree(uintptr(unsafe.Pointer(out.Data)))

	return bytesFromBlob(out), nil
}

var procLocalFree = kernel32.NewProc("LocalFree")

func localFree(ptr uintptr) {
	if ptr == 0 {
		return
	}
	if _, _, err := procLocalFree.Call(ptr); err != nil {
		_ = err
	}
}
