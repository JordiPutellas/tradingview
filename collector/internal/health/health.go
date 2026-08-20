// Package health expone el estado del colector y hace ping saliente.
//
// La métrica que importa es la FRESCURA DEL DATO (segundos desde el último
// aggTrade recibido), no que el proceso viva: un WS puede estar conectado sin
// recibir nada (stream zombi, trampa 5). El ping a healthchecks.io solo se
// emite cuando el dato está fresco; si el stream se para, el ping cesa y la
// alerta salta aunque el proceso siga vivo (RF-6.3, crítico por la ventana
// REST de 48 h).
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type State struct {
	mu           sync.Mutex
	startedAt    time.Time
	wsConnected  bool
	lastTradeAt  time.Time
	lastTradeID  int64
	lastCandleTs int64
	lastWriteAt  time.Time

	// FreshnessMax define "fresco" para status y para el gate del pinger.
	FreshnessMax time.Duration
	// BufferLen y OpenGaps se consultan en cada /health.
	BufferLen func() int
	OpenGaps  func(ctx context.Context) (int, error)
}

func New(freshnessMax time.Duration) *State {
	return &State{startedAt: time.Now().UTC(), FreshnessMax: freshnessMax}
}

func (s *State) SetWSConnected(v bool) {
	s.mu.Lock()
	s.wsConnected = v
	s.mu.Unlock()
}

func (s *State) TradeSeen(id int64) {
	s.mu.Lock()
	s.lastTradeAt = time.Now().UTC()
	s.lastTradeID = id
	s.mu.Unlock()
}

func (s *State) CandleWritten(tsSec int64) {
	s.mu.Lock()
	if tsSec > s.lastCandleTs {
		s.lastCandleTs = tsSec
	}
	s.lastWriteAt = time.Now().UTC()
	s.mu.Unlock()
}

// Fresh indica si el último trade recibido está dentro de FreshnessMax.
func (s *State) Fresh() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.lastTradeAt.IsZero() && time.Since(s.lastTradeAt) <= s.FreshnessMax
}

type report struct {
	Status           string  `json:"status"` // ok | stale
	WSConnected      bool    `json:"ws_connected"`
	LastTradeAt      string  `json:"last_trade_at"`
	LastTradeID      int64   `json:"last_trade_id"`
	DataFreshnessSec float64 `json:"data_freshness_seconds"`
	LastCandleTs     string  `json:"last_candle_ts"`
	LastWriteAt      string  `json:"last_write_at"`
	BufferLen        int     `json:"buffer_len"`
	OpenGaps         int     `json:"open_gaps"`
	UptimeSec        float64 `json:"uptime_seconds"`
}

func (s *State) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rep := report{
			WSConnected: s.wsConnected,
			LastTradeID: s.lastTradeID,
			UptimeSec:   time.Since(s.startedAt).Seconds(),
		}
		if !s.lastTradeAt.IsZero() {
			rep.LastTradeAt = s.lastTradeAt.Format(time.RFC3339Nano)
			rep.DataFreshnessSec = time.Since(s.lastTradeAt).Seconds()
		}
		if s.lastCandleTs > 0 {
			rep.LastCandleTs = time.Unix(s.lastCandleTs, 0).UTC().Format(time.RFC3339)
		}
		if !s.lastWriteAt.IsZero() {
			rep.LastWriteAt = s.lastWriteAt.Format(time.RFC3339Nano)
		}
		fresh := s.wsConnected && !s.lastTradeAt.IsZero() && time.Since(s.lastTradeAt) <= s.FreshnessMax
		s.mu.Unlock()
		rep.Status = "ok"
		if !fresh {
			rep.Status = "stale"
		}
		if s.BufferLen != nil {
			rep.BufferLen = s.BufferLen()
		}
		if s.OpenGaps != nil {
			if n, err := s.OpenGaps(r.Context()); err == nil {
				rep.OpenGaps = n
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if rep.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(rep)
	})
	return mux
}

// Serve arranca el endpoint /health hasta que ctx se cancele.
func (s *State) Serve(ctx context.Context, addr string) {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("health server", "err", err)
	}
}

// RunPinger hace GET a url cada interval MIENTRAS el dato esté fresco.
func (s *State) RunPinger(ctx context.Context, url string, interval time.Duration) {
	if url == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !s.Fresh() {
				slog.Warn("healthcheck ping suppressed: data not fresh")
				continue
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if resp, err := client.Do(req); err != nil {
				slog.Warn("healthcheck ping failed", "err", err)
			} else {
				resp.Body.Close()
			}
		}
	}
}
