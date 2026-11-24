package util

import (
	"context"
	"log/slog"
	"net"
	"time"
)

const (
	resolveTimeout = 4 * time.Second

	dialTimeout = 5 * time.Second

	onlineInterval = 3 * time.Second
)

func IsOnline() bool {
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(ctx, "discord.com")
	if err != nil || len(addrs) == 0 {
		return false
	}
	return dialOK("discord.com:443", dialTimeout)
}

func dialOK(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	if cerr := conn.Close(); cerr != nil {
		slog.Debug("netcheck: cierre de conexión falló", "addr", addr, "err", cerr)
	}
	return true
}

func WaitUntilOnline(ctx context.Context) {
	ticker := time.NewTicker(onlineInterval)
	defer ticker.Stop()

	for attempt := 1; ; attempt++ {
		if IsOnline() {
			if attempt > 1 {
				slog.Info("netcheck: red disponible", "intentos", attempt)
			}
			return
		}

		if attempt%2 == 1 {
			if err := flushResolverCache(); err != nil {
				slog.Debug("netcheck: no se pudo limpiar la caché DNS", "err", err)
			} else {
				slog.Debug("netcheck: caché DNS limpiada", "intento", attempt)
			}
		}

		slog.Debug("netcheck: sin conexión, reintento en 3s", "intento", attempt)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
