package util

import (
	"context"
	"log/slog"
	"net"
	"time"
)

const (
	// resolveTimeout acota la consulta DNS. Tras un bugcheck la pila de red
	// responde tarde y sin tope el reintento se come el reloj.
	resolveTimeout = 4 * time.Second

	// dialTimeout acota el TCP posterior a la resolución.
	dialTimeout = 5 * time.Second

	// onlineInterval es el sondeo mientras no hay red. Tres segundos, no diez:
	// el arranque tras pantalla azul se mide en segundos.
	onlineInterval = 3 * time.Second
)

// IsOnline exige que discord.com resuelva y acepte TCP.
//
// No vale con que conteste una IP suelta. El cliente DNS de Windows guarda el
// NXDOMAIN del primer intento —cuando la red todavía está dormida— y lo sirve
// durante minutos. Un sondeo a 8.8.8.8 decía "hay red" mientras el gateway
// seguía sin resolver, y el agente se pasaba cuatro minutos reintentando contra
// un nombre que su propia caché daba por inexistente.
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

// WaitUntilOnline bloquea hasta que IsOnline devuelve true o el contexto se
// cancela.
//
// Limpia la caché DNS en cada intento impar. Ese flush es la pieza que devuelve
// el agente en segundos después de un bugcheck: el primer lookup falla porque
// la pila aún no está lista, Windows cachea el NXDOMAIN, y sin limpiarlo el
// nombre sigue "no such host" hasta que expira el TTL del registro.
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
