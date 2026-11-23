//go:build windows

// Comando winres: inyecta en un .exe los recursos que hacen que la app parezca
// terminada — manifiesto con controles comunes v6 y DPI por monitor, icono en
// todos los tamaños y bloque de versión.
//
// Se ejecuta después de go build. No usa rc.exe ni el SDK: escribe los
// recursos con BeginUpdateResource/UpdateResource de kernel32, y dibuja el
// icono píxel a píxel para no depender de archivos binarios en el repo.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24

	langNeutral = 0
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procBeginUpdateResource = kernel32.NewProc("BeginUpdateResourceW")
	procUpdateResource      = kernel32.NewProc("UpdateResourceW")
	procEndUpdateResource   = kernel32.NewProc("EndUpdateResourceW")
)

func main() {
	exe := flag.String("exe", "", "ejecutable a firmar con recursos (obligatorio)")
	name := flag.String("name", "GoVoid Studio", "nombre del producto")
	company := flag.String("company", "GoVoid", "fabricante")
	version := flag.String("version", "1.0.0.0", "versión, cuatro números")
	iconOnly := flag.Bool("icon-only", false, "solo icono, sin manifiesto ni versión")
	flag.Parse()

	if *exe == "" {
		fail("falta -exe")
	}
	if _, err := os.Stat(*exe); err != nil {
		fail("no encuentro %s: %v", *exe, err)
	}

	if err := apply(*exe, *name, *company, *version, *iconOnly); err != nil {
		fail("%v", err)
	}
	fmt.Printf("%s  %s %s  icono + manifiesto + versión\n", filepath.Base(*exe), *name, *version)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "winres: "+format+"\n", args...)
	os.Exit(1)
}

func apply(path, name, company, version string, iconOnly bool) error {
	handle, _, callErr := procBeginUpdateResource.Call(
		uintptr(unsafe.Pointer(u16(path))), 0)
	if handle == 0 {
		return fmt.Errorf("BeginUpdateResource: %v", callErr)
	}

	abort := true
	defer func() {
		discard := uintptr(0)
		if abort {
			discard = 1
		}
		procEndUpdateResource.Call(handle, discard)
	}()

	images := []int{256, 128, 64, 48, 32, 24, 16}
	group := make([]byte, 6+14*len(images))
	binary.LittleEndian.PutUint16(group[2:], 1)
	binary.LittleEndian.PutUint16(group[4:], uint16(len(images)))

	for index, size := range images {
		pixels := renderIcon(size)
		dib := bitmapDIB(pixels, size)
		id := index + 1
		if err := update(handle, rtIcon, uintptr(id), dib); err != nil {
			return err
		}

		entry := 6 + 14*index
		width := byte(size)
		height := byte(size)
		if size >= 256 {
			width, height = 0, 0
		}
		group[entry] = width
		group[entry+1] = height
		group[entry+2] = 0
		group[entry+3] = 0
		binary.LittleEndian.PutUint16(group[entry+4:], 1)
		binary.LittleEndian.PutUint16(group[entry+6:], 32)
		binary.LittleEndian.PutUint32(group[entry+8:], uint32(len(dib)))
		binary.LittleEndian.PutUint16(group[entry+12:], uint16(id))
	}
	if err := update(handle, rtGroupIcon, 1, group); err != nil {
		return err
	}

	if !iconOnly {
		if err := update(handle, rtManifest, 1, []byte(manifest)); err != nil {
			return err
		}
		versionInfo, err := buildVersion(name, company, version)
		if err != nil {
			return err
		}
		if err := update(handle, rtVersion, 1, versionInfo); err != nil {
			return err
		}
	}

	abort = false
	return nil
}

func update(handle uintptr, kind int, id uintptr, data []byte) error {
	var pointer uintptr
	if len(data) > 0 {
		pointer = uintptr(unsafe.Pointer(&data[0]))
	}
	ok, _, callErr := procUpdateResource.Call(handle, uintptr(kind), id, langNeutral,
		pointer, uintptr(len(data)))
	if ok == 0 {
		return fmt.Errorf("UpdateResource tipo %d: %v", kind, callErr)
	}
	return nil
}

// --- icono ---

// renderIcon dibuja la marca: cuadro redondeado oscuro con un chevrón y una
// barra en verde. Se muestrea 4x4 por píxel para que los bordes no salgan
// escalonados a tamaño pequeño.
func renderIcon(size int) []byte {
	const samples = 4
	const total = float64(samples * samples)

	top := [3]float64{15, 23, 42}
	bottom := [3]float64{2, 6, 23}
	mark := [3]float64{16, 185, 129}

	out := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var inside, painted float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/samples) / float64(size)
					py := (float64(y) + (float64(sy)+0.5)/samples) / float64(size)
					if !roundedRect(px, py, 0.02, 0.02, 0.98, 0.98, 0.22) {
						continue
					}
					inside++
					if isMark(px, py) {
						painted++
					}
				}
			}
			if inside == 0 {
				continue
			}

			alpha := inside / total
			blend := painted / inside
			gradient := float64(y) / float64(size-1)

			red := top[0]*(1-gradient) + bottom[0]*gradient
			green := top[1]*(1-gradient) + bottom[1]*gradient
			blue := top[2]*(1-gradient) + bottom[2]*gradient
			red = red*(1-blend) + mark[0]*blend
			green = green*(1-blend) + mark[1]*blend
			blue = blue*(1-blend) + mark[2]*blend

			offset := (y*size + x) * 4
			out[offset+0] = byte(blue + 0.5)
			out[offset+1] = byte(green + 0.5)
			out[offset+2] = byte(red + 0.5)
			out[offset+3] = byte(alpha*255 + 0.5)
		}
	}
	return out
}

func isMark(px, py float64) bool {
	const thickness = 0.13
	if segmentDistance(px, py, 0.30, 0.28, 0.63, 0.50) < thickness/2 {
		return true
	}
	if segmentDistance(px, py, 0.63, 0.50, 0.30, 0.72) < thickness/2 {
		return true
	}
	return segmentDistance(px, py, 0.44, 0.74, 0.72, 0.74) < 0.09/2
}

func segmentDistance(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	length := dx*dx + dy*dy
	t := 0.0
	if length > 0 {
		t = ((px-ax)*dx + (py-ay)*dy) / length
		t = math.Max(0, math.Min(1, t))
	}
	ex, ey := ax+t*dx, ay+t*dy
	return math.Hypot(px-ex, py-ey)
}

func roundedRect(px, py, left, top, right, bottom, radius float64) bool {
	if px < left || px > right || py < top || py > bottom {
		return false
	}
	cx := math.Max(left+radius, math.Min(px, right-radius))
	cy := math.Max(top+radius, math.Min(py, bottom-radius))
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= radius*radius
}

// bitmapDIB empaqueta los píxeles como BITMAPINFOHEADER + BGRA ascendente +
// máscara AND. Es el formato que espera RT_ICON.
func bitmapDIB(pixels []byte, size int) []byte {
	stride := size * 4
	maskStride := ((size + 31) / 32) * 4
	out := make([]byte, 40+size*stride+maskStride*size)

	binary.LittleEndian.PutUint32(out[0:], 40)
	binary.LittleEndian.PutUint32(out[4:], uint32(size))
	binary.LittleEndian.PutUint32(out[8:], uint32(size*2))
	binary.LittleEndian.PutUint16(out[12:], 1)
	binary.LittleEndian.PutUint16(out[14:], 32)
	binary.LittleEndian.PutUint32(out[20:], uint32(size*stride))

	for y := 0; y < size; y++ {
		source := (size - 1 - y) * stride
		copy(out[40+y*stride:40+(y+1)*stride], pixels[source:source+stride])
	}
	return out
}

// --- versión ---

func buildVersion(product, company, version string) ([]byte, error) {
	parts := strings.Split(version, ".")
	if len(parts) != 4 {
		return nil, fmt.Errorf("la versión debe ser a.b.c.d, no %q", version)
	}
	numbers := make([]uint16, 4)
	for index, part := range parts {
		value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 16)
		if err != nil {
			return nil, fmt.Errorf("versión inválida %q: %w", version, err)
		}
		numbers[index] = uint16(value)
	}

	fixed := make([]byte, 52)
	binary.LittleEndian.PutUint32(fixed[0:], 0xFEEF04BD)
	binary.LittleEndian.PutUint32(fixed[4:], 0x00010000)
	binary.LittleEndian.PutUint32(fixed[8:], uint32(numbers[0])<<16|uint32(numbers[1]))
	binary.LittleEndian.PutUint32(fixed[12:], uint32(numbers[2])<<16|uint32(numbers[3]))
	binary.LittleEndian.PutUint32(fixed[16:], uint32(numbers[0])<<16|uint32(numbers[1]))
	binary.LittleEndian.PutUint32(fixed[20:], uint32(numbers[2])<<16|uint32(numbers[3]))
	binary.LittleEndian.PutUint32(fixed[24:], 0x3F)
	binary.LittleEndian.PutUint32(fixed[28:], 0)
	binary.LittleEndian.PutUint32(fixed[32:], 0x40004) // VOS_NT_WINDOWS32
	binary.LittleEndian.PutUint32(fixed[36:], 1)       // VFT_APP
	binary.LittleEndian.PutUint32(fixed[40:], 0)
	binary.LittleEndian.PutUint32(fixed[44:], 0)

	table := versionBlock(0, 1, "StringFileInfo", nil,
		versionBlock(0, 1, "040904B0", nil,
			versionString("CompanyName", company),
			versionString("FileDescription", product),
			versionString("FileVersion", version),
			versionString("InternalName", "GoVoidStudio"),
			versionString("OriginalFilename", "GoVoidStudio.exe"),
			versionString("ProductName", product),
			versionString("ProductVersion", version),
			versionString("LegalCopyright", "Copyright (c) 2026 "+company),
		),
	)

	root := versionBlock(52, 0, "VS_VERSION_INFO", fixed, table)
	return root, nil
}

func versionBlock(valueLength int, kind uint16, key string, value []byte, children ...[]byte) []byte {
	headerSize := 6 + (len(key)+1)*2
	body := align4(headerSize) + len(value)
	for _, child := range children {
		body = align4(body) + len(child)
	}

	out := make([]byte, body)
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	binary.LittleEndian.PutUint16(out[2:], uint16(valueLength))
	binary.LittleEndian.PutUint16(out[4:], kind)
	putUTF16(out[6:], key)

	offset := align4(headerSize)
	copy(out[offset:], value)

	offset = align4(offset + len(value))
	for _, child := range children {
		copy(out[offset:], child)
		offset = align4(offset + len(child))
	}
	return out
}

func versionString(name, value string) []byte {
	encoded := utf16(value)
	headerSize := 6 + (len(name)+1)*2
	body := align4(headerSize) + len(encoded)*2
	out := make([]byte, body)
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	binary.LittleEndian.PutUint16(out[2:], uint16(len(encoded)))
	binary.LittleEndian.PutUint16(out[4:], 1)
	putUTF16(out[6:], name)
	putUTF16(out[align4(headerSize):], value)
	return out
}

// putUTF16 escribe una cadena UTF-16LE terminada en cero sobre un tramo de
// bytes. Los bloques de versión son bytes, no palabras: así se evita tener que
// convertir entre []byte y []uint16.
func putUTF16(dst []byte, value string) {
	for index, unit := range utf16(value) {
		offset := index * 2
		if offset+2 > len(dst) {
			return
		}
		binary.LittleEndian.PutUint16(dst[offset:], unit)
	}
}

func align4(value int) int { return (value + 3) &^ 3 }

func utf16(value string) []uint16 {
	runes := []rune(value)
	out := make([]uint16, 0, len(runes)+1)
	for _, r := range runes {
		out = append(out, uint16(r))
	}
	return append(out, 0)
}

func u16(value string) *uint16 {
	pointer, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		empty := []uint16{0}
		return &empty[0]
	}
	return pointer
}

const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="GoVoid.Studio" version="1.0.0.0" processorArchitecture="*"/>
  <description>GoVoid Studio</description>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0"
        processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
      <longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>
    </windowsSettings>
  </application>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
</assembly>
`
