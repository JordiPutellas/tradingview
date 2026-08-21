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
	"jputellas.dev/btcdash/collector/internal/fixed"
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

// Klines1m devuelve las klines 1m oficiales con open_time en [startMs, endMs),
// paginando por startTime. A diferencia de aggTrades, paginar klines por
// tiempo es seguro (buckets fijos de 60s, sin ambigüedad de milisegundo) y el
// endpoint no tiene ventana de 48 h: llega hasta el origen del par (2019).
// Peso 10/req con limit=1500; se pacea a ~4 req/s.
func (r *REST) Klines1m(ctx context.Context, startMs, endMs int64, fn func(Kline1m) error) (int64, error) {
	var total int64
	cursor := startMs
	for cursor < endMs {
		url := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1m&startTime=%d&limit=1500", r.BaseURL, r.Symbol, cursor)
		body, err := r.getWithRetry(ctx, url)
		if err != nil {
			return total, err
		}
		var rows [][]json.RawMessage
		if err := json.Unmarshal(body, &rows); err != nil {
			return total, fmt.Errorf("rest klines: bad JSON: %w", err)
		}
		if len(rows) == 0 {
			return total, nil
		}
		for _, row := range rows {
			if len(row) < 10 {
				return total, fmt.Errorf("rest klines: short row (%d cols)", len(row))
			}
			var openMs, count int64
			var o, h, l, c, v, tb string
			if err := json.Unmarshal(row[0], &openMs); err != nil {
				return total, err
			}
			if openMs >= endMs {
				return total, nil
			}
			for i, dst := range map[int]*string{1: &o, 2: &h, 3: &l, 4: &c, 5: &v, 9: &tb} {
				if err := json.Unmarshal(row[i], dst); err != nil {
					return total, err
				}
			}
			if err := json.Unmarshal(row[8], &count); err != nil {
				return total, err
			}
			k := Kline1m{OpenMs: openMs, Count: count}
			var perr error
			for _, p := range []struct {
				dst *int64
				s   string
			}{{&k.Open, o}, {&k.High, h}, {&k.Low, l}, {&k.Close, c}, {&k.Volume, v}, {&k.TakerBuy, tb}} {
				if *p.dst, perr = fixed.Parse(p.s); perr != nil {
					return total, perr
				}
			}
			if err := fn(k); err != nil {
				return total, err
			}
			total++
			cursor = openMs + 60_000
		}
	}
	return total, nil
}

func (r *REST) getWithRetry(ctx context.Context, url string) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		select {
		case <-time.After(250 * time.Millisecond):
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
			return body, nil
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
