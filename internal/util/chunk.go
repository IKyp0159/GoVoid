package util

import (
	"strings"
	"unicode/utf8"
)

// DiscordMessageLimit deja margen bajo los 2000 caracteres de Discord.
const DiscordMessageLimit = 1990

// Chunk parte un texto en trozos de como máximo size caracteres,
// cortando preferentemente por salto de línea para no romper bloques.
//
// Los cortes se ajustan a límites de rune: partir un carácter multibyte deja
// bytes inválidos y Discord los muestra como replacement characters, además de
// romper cualquier bloque de código que los contenga.
func Chunk(s string, size int) []string {
	if size <= 0 || size > DiscordMessageLimit {
		size = DiscordMessageLimit
	}
	if s == "" {
		return []string{""}
	}

	remaining := s
	out := make([]string, 0, len(s)/size+1)
	for len(remaining) > size {
		cut := strings.LastIndexByte(remaining[:size], '\n')
		if cut <= 0 {
			cut = size
		}
		cut = runeBoundary(remaining, cut)
		if cut <= 0 {
			cut = runeBoundary(remaining, size)
		}
		out = append(out, remaining[:cut])
		remaining = strings.TrimPrefix(remaining[cut:], "\n")
	}
	if remaining != "" {
		out = append(out, remaining)
	}
	return out
}

// SafeCut devuelve s[:n] sin partir un rune. n se acota al tamaño de s.
func SafeCut(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	return s[:runeBoundary(s, n)]
}

// runeBoundary retrocede n hasta el inicio del rune que lo contiene.
func runeBoundary(s string, n int) int {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}
