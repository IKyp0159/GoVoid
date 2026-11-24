package util

import (
	"strings"
	"unicode/utf8"
)

const DiscordMessageLimit = 1990

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
		if cut = runeBoundary(remaining, cut); cut <= 0 {
			cut = size
		}
		out = append(out, remaining[:cut])
		remaining = strings.TrimPrefix(remaining[cut:], "\n")
	}
	if remaining != "" {
		out = append(out, remaining)
	}
	return out
}

func SafeCut(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	return s[:runeBoundary(s, n)]
}

func runeBoundary(s string, n int) int {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}
