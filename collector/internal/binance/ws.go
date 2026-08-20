package binance

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/coder/websocket"

	"jputellas.dev/btcdash/collector/internal/candle"
)

// WSClient consume el stream <symbol>@aggTrade con reconexión automática.
type WSClient struct {
	BaseURL string // wss://fstream.binance.com/ws
	Symbol  string // BTCUSDT

	// ReadIdleTimeout: si no llega NINGÚN mensaje en este tiempo se fuerza
	// reconexión (stream zombi, trampa 5). BTCUSDT perp tiene trades casi
	// cada segundo; 90s de silencio es anómalo de sobra.
	ReadIdleTimeout time.Duration

	// OnConnect se llama en cada (re)conexión establecida.
	OnConnect func()
	// OnDisconnect se llama al perder la conexión, con el error causante.
	OnDisconnect func(err error)
}

// Run se conecta y entrega cada aggTrade a onTrade, reconectando con backoff
// exponencial y jitter hasta que ctx se cancele. onTrade se llama siempre
// desde la misma goroutine (el pipeline de agregación exige orden).
//
// La desconexión forzosa a las 24 h del servidor llega como cierre normal y
// sigue el mismo camino: reconectar y dejar que el detector de huecos haga su
// trabajo (RF-1.2/RF-1.3).
func (w *WSClient) Run(ctx context.Context, onTrade func(candle.AggTrade)) {
	if w.ReadIdleTimeout == 0 {
		w.ReadIdleTimeout = 90 * time.Second
	}
	url := w.BaseURL + "/" + strings.ToLower(w.Symbol) + "@aggTrade"
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := w.readLoop(ctx, url, onTrade)
		if ctx.Err() != nil {
			return
		}
		if w.OnDisconnect != nil {
			w.OnDisconnect(err)
		}
		connectedFor := time.Since(start)
		if connectedFor > time.Minute {
			backoff = time.Second // conexión estable previa: resetea el backoff
		}
		if connectedFor > 23*time.Hour {
			slog.Info("ws disconnect after ~24h (expected forced disconnect)", "connected_for", connectedFor)
		} else {
			slog.Warn("ws disconnected", "err", err, "connected_for", connectedFor, "retry_in", backoff)
		}
		jitter := time.Duration(rand.Int64N(int64(backoff / 2)))
		select {
		case <-time.After(backoff/2 + jitter):
		case <-ctx.Done():
			return
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func (w *WSClient) readLoop(ctx context.Context, url string, onTrade func(candle.AggTrade)) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	conn.SetReadLimit(1 << 20)
	slog.Info("ws connected", "url", url)
	if w.OnConnect != nil {
		w.OnConnect()
	}
	// Los ping del servidor (cada ~3 min) los responde la librería durante
	// Read; mientras leamos continuamente, el pong sale dentro de la ventana.
	for {
		readCtx, cancel := context.WithTimeout(ctx, w.ReadIdleTimeout)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		// EventTime es imprescindible aunque no se use: sin un campo con tag
		// exacto `json:"E"`, el "E" numérico del mensaje casaría por
		// case-insensitive con `json:"e"` (string) y rompería el unmarshal.
		var ev struct {
			Type      string `json:"e"`
			EventTime int64  `json:"E"`
			aggTradePayload
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			slog.Error("ws: bad message", "err", err, "payload", string(data))
			continue
		}
		if ev.Type != "aggTrade" {
			continue
		}
		t, err := ev.toCandle()
		if err != nil {
			slog.Error("ws: bad trade", "err", err)
			continue
		}
		onTrade(t)
	}
}
