//go:build windows

package win32

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"unsafe"
)

var (
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procGetDeviceCaps          = gdi32.NewProc("GetDeviceCaps")
)

const (
	smCXScreen   = 0
	smCYScreen   = 1
	horzRes      = 8
	vertRes      = 10
	srcCopy      = 0x00CC0020
	biRGB        = 0
	dibRGBColors = 0
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type rgbQuad struct {
	Blue     byte
	Green    byte
	Red      byte
	Reserved byte
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]rgbQuad
}

// CaptureScreen captura el escritorio primario vía GDI BitBlt y devuelve
// los bytes de un PNG listo para adjuntar.
func CaptureScreen() ([]byte, error) {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("screenshot: GetDC falló")
	}
	defer procReleaseDC.Call(0, screenDC)

	// GetDeviceCaps devuelve píxeles físicos; GetSystemMetrics puede venir
	// escalado por DPI y recortaría la captura.
	width, _, _ := procGetDeviceCaps.Call(screenDC, horzRes)
	height, _, _ := procGetDeviceCaps.Call(screenDC, vertRes)
	if width == 0 || height == 0 {
		width, _, _ = procGetSystemMetrics.Call(smCXScreen)
		height, _, _ = procGetSystemMetrics.Call(smCYScreen)
	}
	if width == 0 || height == 0 {
		return nil, fmt.Errorf("screenshot: resolución de pantalla inválida (%dx%d)", width, height)
	}

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleDC falló")
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, width, height)
	if bitmap == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleBitmap falló")
	}
	defer procDeleteObject.Call(bitmap)

	old, _, _ := procSelectObject.Call(memDC, bitmap)
	if old == 0 {
		return nil, fmt.Errorf("screenshot: SelectObject falló")
	}

	copied, _, _ := procBitBlt.Call(memDC, 0, 0, width, height, screenDC, 0, 0, srcCopy)
	if copied == 0 {
		return nil, fmt.Errorf("screenshot: BitBlt falló")
	}

	// GetDIBits exige que el bitmap no esté seleccionado en el DC.
	procSelectObject.Call(memDC, old)

	w := int32(width)
	h := int32(height)

	info := bitmapInfo{}
	info.Header.Size = uint32(unsafe.Sizeof(info.Header))
	info.Header.Width = w
	info.Header.Height = -h // top-down: la primera fila es la de arriba
	info.Header.Planes = 1
	info.Header.BitCount = 32
	info.Header.Compression = biRGB

	buf := make([]byte, int(width)*int(height)*4)
	rows, _, _ := procGetDIBits.Call(
		memDC,
		bitmap,
		0,
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
	)
	if rows == 0 {
		return nil, fmt.Errorf("screenshot: GetDIBits falló")
	}

	img := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	for i := 0; i+3 < len(buf); i += 4 {
		img.Pix[i] = buf[i+2]   // R
		img.Pix[i+1] = buf[i+1] // G
		img.Pix[i+2] = buf[i]   // B
		img.Pix[i+3] = 0xFF     // A
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("screenshot: png: %w", err)
	}
	return out.Bytes(), nil
}
