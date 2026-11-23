//go:build windows

package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Upload descarga una URL a %TEMP% y devuelve la ruta local.
func Upload(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	dest, written, err := fetchToTemp(StringOption(i, "url"))
	if err != nil {
		Finish(s, i, "❌ "+err.Error())
		return
	}

	Finish(s, i, fmt.Sprintf("📤 Guardado en `%s` (%d bytes).", dest, written))
}

// fetchToTemp descarga rawURL a un archivo temporal y devuelve la ruta y los
// bytes escritos. El nombre sale del último segmento de la URL; si no hay, se
// genera uno con la marca de tiempo.
func fetchToTemp(rawURL string) (string, int64, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return "", 0, errors.New("URL inválida.")
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", 0, fmt.Errorf("Descarga falló: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			_ = cerr
		}
	}()

	if resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("El servidor respondió HTTP %d.", resp.StatusCode)
	}

	name := path.Base(parsed.Path)
	// path.Base puede devolver ".", "..", "/" o un nombre con separadores
	// codificados; se limpia a un nombre simple antes de tocar el disco.
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" ||
		strings.ContainsAny(name, `\/:*?"<>|`) {
		name = fmt.Sprintf("upload_%d.bin", time.Now().Unix())
	}
	dest := filepath.Join(os.TempDir(), name)

	out, err := os.Create(dest)
	if err != nil {
		return "", 0, fmt.Errorf("No se pudo crear el archivo: %w", err)
	}

	written, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(dest)
		return "", 0, fmt.Errorf("Error escribiendo archivo: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(dest)
		return "", 0, fmt.Errorf("Error cerrando archivo: %w", closeErr)
	}

	return dest, written, nil
}
