//go:build windows

package win32

import "testing"

// TestSpecialLabelCubreNavegacion fija el bug del dump: 0x27 es la flecha
// derecha, no un VK anónimo. Si alguna se cae del mapa, vuelve el [VK:0x27].
func TestSpecialLabelCubreNavegacion(t *testing.T) {
	cases := map[uint32]string{
		0x25: "[LEFT]",
		0x26: "[UP]",
		0x27: "[RIGHT]",
		0x28: "[DOWN]",
		0x24: "[HOME]",
		0x23: "[END]",
		0x21: "[PGUP]",
		0x22: "[PGDN]",
		0x2D: "[INS]",
		0x2E: "[DEL]",
		0x2C: "[PRTSC]",
		0x5D: "[MENU]",
		0x0D: "[ENTER]",
		0x09: "[TAB]",
		0x08: "[BKSP]",
		0x1B: "[ESC]",
		0x5B: "[WIN]",
		0x5C: "[WIN]",
		0x70: "[F1]",
		0x7B: "[F12]",
	}
	for vk, want := range cases {
		got, ok := specialLabel(vk)
		if !ok || got != want {
			t.Fatalf("VK 0x%02X: esperado %q, obtenido %q (ok=%v)", vk, want, got, ok)
		}
	}
}

// TestSpecialLabelNoInventa: una tecla desconocida debe seguir cayendo al
// genérico de record, no a una etiqueta falsa.
func TestSpecialLabelNoInventa(t *testing.T) {
	if label, ok := specialLabel(0xDF); ok {
		t.Fatalf("VK 0xDF no debería tener etiqueta fija, salió %q", label)
	}
}

func TestCtrlCombo(t *testing.T) {
	cases := map[string]string{
		"[RIGHT]": "[CTRL+RIGHT]",
		"[LEFT]":  "[CTRL+LEFT]",
		"[F5]":    "[CTRL+F5]",
		"[HOME]":  "[CTRL+HOME]",
	}
	for label, want := range cases {
		if got := ctrlCombo(label); got != want {
			t.Fatalf("%s: esperado %s, obtenido %s", label, want, got)
		}
	}
	if ctrlComposable("[VK:0x27]") {
		t.Fatal("[VK:0xNN] no admite prefijo CTRL")
	}
	if !ctrlComposable("[RIGHT]") {
		t.Fatal("[RIGHT] debe admitir prefijo CTRL")
	}
}

// TestModificadoresNoSonTexto: Shift/Ctrl/Alt sueltos no ensucian el dump.
func TestModificadoresNoSonTexto(t *testing.T) {
	for _, vk := range modifierVKs {
		if !isModifierKey(vk) {
			t.Fatalf("VK 0x%02X debería tratarse como modificador", vk)
		}
		if _, ok := specialLabel(vk); ok {
			t.Fatalf("VK 0x%02X es modificador y no debe tener etiqueta", vk)
		}
	}
}
