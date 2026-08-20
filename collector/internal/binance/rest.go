package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"jputellas.dev/btcdash/collector/internal/candle"
)

// REST es el cliente de /fapi para reconciliación de huecos.
//
// Reglas aprendidas en F0:
//   - Paginar SIEMPRE por fromId (RF-2.2): startTime pierde ~0,7% de trades.
//   - La ventana de búsqueda es de ~2 días (error -4166), con startTime Y con
//     fromId. El reconciliador clasifica los huecos antes de llamar aquí.
//   - Peso 20/req, límite 2400/min por IP: ritmo de ~1 req/550ms.
type REST struct {
	BaseURL string
	Symbol  string
	HTTP    *http.Client
}

// ErrOutsideWindow señala el -4166: el rango pedido ya no es accesible por REST.
var ErrOutsideWindow = fmt.Errorf("rest: outside the ~48h search window (-4166)")

func NewREST(baseURL, symbol string) *REST {
	return &REST{BaseURL: baseURL, Symbol: symbol, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// AggTradesFrom devuelve hasta 1000 aggTrades con id >= fromID.
func (r *REST) AggTradesFrom(ctx context.Context, fromID int64) ([]candle.AggTrade, error) {
	return r.fetch(ctx, fmt.Sprintf("fromId=%d", fromID))
}

// AggTradesSince devuelve hasta 1000 aggTrades con T >= sinceMs. Solo se usa
// para sembrar la paginación cuando no se conoce ningún id (arranque en frío
// dentro de la ventana); a partir de ahí, SIEMPRE fromId.
func (r *REST) AggTradesSince(ctx context.Context, sinceMs int64) ([]candle.AggTrade, error) {
	return r.fetch(ctx, fmt.Sprintf("startTime=%d", sinceMs))
}

func (r *REST) fetch(ctx context.Context, params string) ([]candle.AggTrade, error) {
	url := fmt.Sprintf("%s/fapi/v1/aggTrades?symbol=%s&limit=1000&%s", r.BaseURL, r.Symbol, params)
	for attempt := 1; ; attempt++ {
		select {
		case <-time.After(550 * time.Millisecond): // ritmo bajo el límite de peso
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := r.HTTP.Do(req)
		if err != nil {
			if attempt >= 5 {
				return nil, err
			}
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			var raw []aggTradePayload
			if err := json.Unmarshal(body, &raw); err != nil {
				return nil, fmt.Errorf("rest: bad JSON: %w", err)
			}
			out := make([]candle.AggTrade, 0, len(raw))
			for _, p := range raw {
				t, err := p.toCandle()
				if err != nil {
					return nil, err
				}
				out = append(out, t)
			}
			return out, nil
		case resp.StatusCode == 429 || resp.StatusCode == 418:
			wait := 60 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if s, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(s+1) * time.Second
				}
			}
			slog.Warn("rest: rate limited", "status", resp.StatusCode, "wait", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case resp.StatusCode == http.StatusBadRequest && isCode(body, -4166):
			return nil, ErrOutsideWindow
		case resp.StatusCode >= 500 && attempt < 5:
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		default:
			return nil, fmt.Errorf("rest: HTTP %d: %s", resp.StatusCode, body)
		}
	}
}

func isCode(body []byte, code int) bool {
	var e struct {
		Code int `json:"code"`
	}
	return json.Unmarshal(body, &e) == nil && e.Code == code
}
