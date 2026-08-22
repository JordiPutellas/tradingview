package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxBars = 20000 // techo por petición: el frontend pagina (RF-4.1)

type Server struct {
	Pool      *pgxpool.Pool
	Symbol    string
	StaticDir string

	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func New(pool *pgxpool.Pool, symbol, staticDir string) *Server {
	return &Server{Pool: pool, Symbol: symbol, StaticDir: staticDir,
		clients: map[chan []byte]struct{}{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/timeframes", s.handleTimeframes)
	mux.HandleFunc("GET /api/candles", s.handleCandles)
	mux.HandleFunc("GET /api/ws", s.handleWS)
	mux.HandleFunc("GET /api/drawings", s.handleListDrawings)
	mux.HandleFunc("PUT /api/drawings/{id}", s.handlePutDrawing)
	mux.HandleFunc("DELETE /api/drawings/{id}", s.handleDeleteDrawing)
	mux.Handle("GET /", cacheHeaders(http.FileServer(http.Dir(s.StaticDir))))
	return mux
}

// hashedAsset: bundles con hash de contenido en el nombre (app.<hash>.js).
var hashedAsset = regexp.MustCompile(`\.[0-9a-f]{8}\.(js|css)$`)

// cacheHeaders: el HTML se revalida SIEMPRE y los bundles con hash se cachean
// para siempre. En F2b un deploy no se vio hasta abrir una ventana de
// incógnito: el fichero nuevo estaba en el servidor (el grep lo confirmaba)
// pero el navegador seguía sirviendo su copia de "app.js" — misma URL de
// siempre y sin cabeceras que dijeran nada.
func cacheHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hashedAsset.MatchString(r.URL.Path) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok", "symbol": s.Symbol})
}

func (s *Server) handleTimeframes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, Timeframes)
}

// handleCandles: /api/candles?tf=1m&from=<epoch>&to=<epoch>&limit=N
// Sin from: devuelve las últimas `limit` velas hasta `to` (o ahora).
// Respuesta compacta: filas [t, o, h, l, c, v] ordenadas ascendentemente.
func (s *Server) handleCandles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tf, ok := tfByName[q.Get("tf")]
	if !ok {
		http.Error(w, "unknown tf", http.StatusBadRequest)
		return
	}
	limit := int64(1500)
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			limit = min(n, maxBars)
		}
	}
	now := time.Now().Unix()
	to := now + tf.Seconds
	if v := q.Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = n
		}
	}
	from, desc := int64(0), true
	if v := q.Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			from, desc = n, false
		}
	}
	order := "ASC"
	if desc {
		// Sin `from`: últimas N. Se acota el escaneo a limit*bucket con
		// margen 3x (segundos vacíos) para no recorrer la hypertable entera.
		order = "DESC"
		from = to - limit*tf.Seconds*3
	}
	// Suelo obligatorio: 5.000 velas de 12M por 3 son 47.000 años hacia atrás,
	// y to_timestamp() de eso hace estallar la query con "timestamp out of
	// range" (500). Se ve al desplazarse al inicio del histórico en los
	// timeframes grandes, que es justo cuando el frontend pide más pasado.
	if from < 0 {
		from = 0
	}
	if max := now + 10*365*24*3600; to > max {
		to = max
	}
	if to <= from {
		writeJSON(w, [][6]float64{})
		return
	}

	t0 := time.Now()
	rows, err := s.Pool.Query(r.Context(), fmt.Sprintf(tf.query, order), s.Symbol, from, to, limit)
	if err != nil {
		slog.Error("candles query", "tf", tf.Name, "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := make([][6]float64, 0, limit)
	for rows.Next() {
		var t int64
		var o, h, l, c, v float64
		if err := rows.Scan(&t, &o, &h, &l, &c, &v); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		out = append(out, [6]float64{float64(t), o, h, l, c, v})
	}
	if rows.Err() != nil {
		http.Error(w, "rows failed", http.StatusInternalServerError)
		return
	}
	if desc { // invertir a ascendente
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	// El patrón de lecturas es el punto abierto de RAM (F1b): queda medido
	// en el log por si hay que reevaluar.
	if d := time.Since(t0); d > 500*time.Millisecond {
		slog.Warn("slow candles query", "tf", tf.Name, "bars", len(out), "elapsed", d)
	}
	writeJSON(w, out)
}

// --- streaming ---

// Run escucha NOTIFY 'candle_update' (emitido por el colector en cada flush)
// y lo reenvía a todos los clientes WS conectados.
func (s *Server) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := s.listenLoop(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("listen loop restarting", "err", err)
			time.Sleep(2 * time.Second)
		}
	}
	return nil
}

func (s *Server) listenLoop(ctx context.Context) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN candle_update"); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.broadcast([]byte(n.Payload))
	}
}

func (s *Server) broadcast(msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- msg:
		default: // cliente lento: se salta este tick, no se bloquea el resto
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}

// --- dibujos (RF-4.3) ---

func (s *Server) handleListDrawings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(),
		`SELECT id, payload, extract(epoch FROM updated_at)::bigint
		 FROM drawings WHERE symbol=$1 ORDER BY updated_at`, s.Symbol)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type drawing struct {
		ID        string          `json:"id"`
		Payload   json.RawMessage `json:"payload"`
		UpdatedAt int64           `json:"updated_at"`
	}
	out := []drawing{}
	for rows.Next() {
		var d drawing
		if err := rows.Scan(&d.ID, &d.Payload, &d.UpdatedAt); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, out)
}

func (s *Server) handlePutDrawing(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	_, err := s.Pool.Exec(r.Context(),
		`INSERT INTO drawings (id, symbol, payload) VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO UPDATE SET payload=EXCLUDED.payload, updated_at=now()`,
		id, s.Symbol, payload)
	if err != nil {
		slog.Error("put drawing", "err", err)
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteDrawing(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Pool.Exec(r.Context(),
		`DELETE FROM drawings WHERE id=$1 AND symbol=$2`, r.PathValue("id"), s.Symbol); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// Sin validadores, un navegador puede cachear un GET por heurística: velas
	// viejas servidas como nuevas.
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
