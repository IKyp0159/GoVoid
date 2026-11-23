//go:build windows

package main

// Diálogos del sistema y portapapeles. Nada de controles propios donde Windows
// ya tiene el diálogo correcto: el operador reconoce estas ventanas.

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0042
)

// widez convierte a UTF-16 conservando los NUL internos, que es como Windows
// separa los pares de un filtro de archivos.
func widez(value string) []uint16 {
	runes := []rune(value)
	out := make([]uint16, 0, len(runes)+1)
	for _, r := range runes {
		out = append(out, uint16(r))
	}
	return append(out, 0)
}

func (a *App) askOpenFile(title, filter string) string {
	return a.fileDialog(title, filter, "", false)
}

func (a *App) askSaveFile(title, filter, name string) string {
	return a.fileDialog(title, filter, name, true)
}

func (a *App) fileDialog(title, filter, defaultName string, save bool) string {
	buffer := make([]uint16, 4096)
	if defaultName != "" {
		for index, value := range []rune(defaultName) {
			if index >= len(buffer)-1 {
				break
			}
			buffer[index] = uint16(value)
		}
	}

	filterWide := widez(filter)
	titleWide := widez(title)

	dialog := openFileName{
		StructSize:  uint32(unsafe.Sizeof(openFileName{})),
		Owner:       a.hwnd,
		Filter:      &filterWide[0],
		FilterIndex: 1,
		File:        &buffer[0],
		MaxFile:     uint32(len(buffer)),
		Title:       &titleWide[0],
		Flags:       ofnExplorer | ofnPathMustExist | ofnNoChangeDir,
	}
	if save {
		dialog.Flags |= ofnOverwritePrompt
	} else {
		dialog.Flags |= ofnFileMustExist
	}

	var result uintptr
	if save {
		result, _, _ = procGetSaveFileName.Call(uintptr(unsafe.Pointer(&dialog)))
	} else {
		result, _, _ = procGetOpenFileName.Call(uintptr(unsafe.Pointer(&dialog)))
	}
	if result == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer)
}

func (a *App) askFolder(title, initial string) string {
	if err := procCoInitializeEx.Find(); err == nil {
		procCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
	}

	titleWide := widez(title)
	display := make([]uint16, 512)

	info := browseInfo{
		Owner:       a.hwnd,
		DisplayName: &display[0],
		Title:       &titleWide[0],
		Flags:       bifReturnOnlyFSDirs | bifNewDialogStyle | bifEditBox,
	}
	_ = initial

	pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&info)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)

	path := make([]uint16, 1024)
	ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(path)
}

func openURL(url string) {
	verb := u16("open")
	target := u16(url)
	procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, 1)
}

func openFolder(path string) {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return
	}
	verb := u16("open")
	target := u16(path)
	procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, 1)
}

func copyText(text string) {
	if text == "" {
		return
	}
	opened, _, _ := procOpenClipboard.Call(0)
	if opened == 0 {
		return
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	utf, err := windows.UTF16FromString(text)
	if err != nil {
		return
	}
	size := uintptr(len(utf) * 2)
	handle, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if handle == 0 {
		return
	}
	pointer, _, _ := procGlobalLock.Call(handle)
	if pointer == 0 {
		procGlobalFree.Call(handle)
		return
	}
	// RtlMoveMemory copia a una dirección que llega como uintptr. Así no hay
	// conversión uintptr→puntero en Go: la memoria es del montón global de
	// Win32, no del recolector.
	procRtlMoveMemory.Call(pointer, uintptr(unsafe.Pointer(&utf[0])), size)
	procGlobalUnlock.Call(handle)

	if stored, _, _ := procSetClipboardData.Call(cfUnicodeText, handle); stored == 0 {
		procGlobalFree.Call(handle)
	}
}
