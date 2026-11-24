//go:build windows

package win32

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procGetClipboardData    = user32.NewProc("GetClipboardData")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAv = user32.NewProc("IsClipboardFormatAvailable")
)

var (
	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalFree   = kernel32.NewProc("GlobalFree")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	procGlobalSize   = kernel32.NewProc("GlobalSize")
)

const gmemMoveable = 0x0042

func GetClipboardText() (string, error) {
	available, _, _ := procIsClipboardFormatAv.Call(cfUnicodeText)
	if available == 0 {
		return "", errors.New("clipboard: no hay texto disponible")
	}

	opened, _, _ := procOpenClipboard.Call(0)
	if opened == 0 {
		return "", errors.New("clipboard: OpenClipboard falló")
	}
	defer procCloseClipboard.Call()

	handle, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if handle == 0 {
		return "", errors.New("clipboard: GetClipboardData falló")
	}

	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return "", errors.New("clipboard: GlobalLock falló")
	}
	defer procGlobalUnlock.Call(handle)

	size, _, _ := procGlobalSize.Call(handle)
	if size == 0 {
		return "", errors.New("clipboard: GlobalSize devolvió 0")
	}

	words := unsafe.Slice((*uint16)(uintptrToPointer(ptr)), size/2)
	return windows.UTF16ToString(words), nil
}

func SetClipboardText(text string) error {
	utf16Text, err := windows.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("clipboard: UTF16FromString: %w", err)
	}
	size := uintptr(len(utf16Text) * 2)

	mem, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if mem == 0 {
		return errors.New("clipboard: GlobalAlloc falló")
	}

	ptr, _, _ := procGlobalLock.Call(mem)
	if ptr == 0 {
		procGlobalFree.Call(mem)
		return errors.New("clipboard: GlobalLock falló")
	}
	dst := unsafe.Slice((*uint16)(uintptrToPointer(ptr)), len(utf16Text))
	copy(dst, utf16Text)
	procGlobalUnlock.Call(mem)

	opened, _, _ := procOpenClipboard.Call(0)
	if opened == 0 {
		procGlobalFree.Call(mem)
		return errors.New("clipboard: OpenClipboard falló")
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()
	ok, _, _ := procSetClipboardData.Call(cfUnicodeText, mem)
	if ok == 0 {
		procGlobalFree.Call(mem)
		return errors.New("clipboard: SetClipboardData falló")
	}
	return nil
}
