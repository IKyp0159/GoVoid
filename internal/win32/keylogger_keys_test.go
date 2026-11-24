//go:build windows

package win32

import "testing"

func TestSilentKey(t *testing.T) {
	mudos := []uint32{
		vkShift, 0xA0, 0xA1,
		vkControl, 0xA2, 0xA3,
		vkMenu, 0xA4, 0xA5,
		vkCapital,
		vkTab, vkEscape,
		vkLWin, vkRWin, 0x5D,
		0x25, 0x26, 0x27, 0x28,
		0x24, 0x23, 0x21, 0x22,
		0x2D, 0x2E, 0x2C, 0x13,
		0x90, 0x91,
		0xAD, 0xAE, 0xAF,
		0x70, 0x75, 0x7B, 0x87,
		0xA6, 0xB7,
	}
	for _, vk := range mudos {
		if !silentKey(vk) {
			t.Fatalf("VK 0x%02X debería ignorarse", vk)
		}
	}

	escriben := []uint32{'A', 'Z', '1', '0', 0x20, 0xDE, 0xBA, 0xBB, 0xBF, 0xC0}
	for _, vk := range escriben {
		if silentKey(vk) {
			t.Fatalf("VK 0x%02X es imprimible y no puede ignorarse", vk)
		}
	}
}

func TestBackspaceYEnterNoSonSilenciosas(t *testing.T) {
	if silentKey(vkBack) {
		t.Fatal("BKSP necesita pasar para borrar del buffer")
	}
	if silentKey(vkReturn) {
		t.Fatal("ENTER necesita pasar para escribir el salto de línea")
	}
}
