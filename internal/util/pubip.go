package util

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

func PublicIP(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return "desconocida"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "desconocida"
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			_ = cerr
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "desconocida"
	}
	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "desconocida"
	}
	return ip
}
