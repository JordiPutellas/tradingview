package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

// CRUD de alertas de precio (F5, bloque 3). Calcado del de dibujos: el id lo
// genera el cliente (crypto.randomUUID), el PUT es un upsert y todo va
// acotado por símbolo. Detrás está Cloudflare Access, así que no hay auth
// propia — igual que en /api/drawings.
//
// Los niveles viajan como número decimal y se guardan en punto fijo 1e8. Con
// precios de cinco cifras y ocho decimales el double da exacto (1e13 < 2^53),
// así que no hace falta pasarlos como cadena.

const e8 = 100_000_000.0

type alertaJSON struct {
	ID           string  `json:"id"`
	Level        float64 `json:"level"`
	Direction    string  `json:"direction"`
	Mode         string  `json:"mode"`
	Status       string  `json:"status"`
	Note         string  `json:"note"`
	DrawingID    *string `json:"drawing_id"`
	DrawingPoint int     `json:"drawing_point"`
	RearmBps     int     `json:"rearm_bps"`
	CooldownSec  int     `json:"cooldown_sec"`
	MaxPerDay    int     `json:"max_per_day"`
	FiredCount   int     `json:"fired_count"`
	LastFiredAt  *int64  `json:"last_fired_at"`
	CreatedAt    int64   `json:"created_at"`
}

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), `
		SELECT id::text, level, direction, mode, status, note, drawing_id::text, drawing_point,
		       rearm_bps, cooldown_sec, max_per_day, fired_count,
		       extract(epoch FROM last_fired_at)::bigint, extract(epoch FROM created_at)::bigint
		FROM alerts WHERE symbol=$1 ORDER BY created_at`, s.Symbol)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []alertaJSON{}
	for rows.Next() {
		var a alertaJSON
		var nivel int64
		if err := rows.Scan(&a.ID, &nivel, &a.Direction, &a.Mode, &a.Status, &a.Note,
			&a.DrawingID, &a.DrawingPoint, &a.RearmBps, &a.CooldownSec, &a.MaxPerDay,
			&a.FiredCount, &a.LastFiredAt, &a.CreatedAt); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		a.Level = float64(nivel) / e8
		out = append(out, a)
	}
	writeJSON(w, out)
}

func (s *Server) handlePutAlert(w http.ResponseWriter, r *http.Request) {
	var a alertaJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&a); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := validarAlerta(&a); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	nivel := int64(math.Round(a.Level * e8))
	// side se pone a NULL en cada escritura: cambiar el nivel obliga a
	// resembrar el lado, o una alerta movida por encima del precio dispararía
	// en cuanto el motor la mirase.
	_, err := s.Pool.Exec(r.Context(), `
		INSERT INTO alerts (id, symbol, level, direction, mode, status, note, drawing_id,
		                    drawing_point, rearm_bps, cooldown_sec, max_per_day)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
		  level=EXCLUDED.level, direction=EXCLUDED.direction, mode=EXCLUDED.mode,
		  status=EXCLUDED.status, note=EXCLUDED.note, drawing_id=EXCLUDED.drawing_id,
		  drawing_point=EXCLUDED.drawing_point, rearm_bps=EXCLUDED.rearm_bps,
		  cooldown_sec=EXCLUDED.cooldown_sec, max_per_day=EXCLUDED.max_per_day,
		  side=NULL, updated_at=now()`,
		id, s.Symbol, nivel, a.Direction, a.Mode, a.Status, a.Note, a.DrawingID,
		a.DrawingPoint, a.RearmBps, a.CooldownSec, a.MaxPerDay)
	if err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validarAlerta(a *alertaJSON) error {
	if !(a.Level > 0) || math.IsInf(a.Level, 0) || math.IsNaN(a.Level) {
		return fmt.Errorf("nivel inválido")
	}
	if a.Direction == "" {
		a.Direction = "any"
	}
	if a.Mode == "" {
		a.Mode = "once"
	}
	if a.Status == "" {
		a.Status = "armed"
	}
	switch a.Direction {
	case "up", "down", "any":
	default:
		return fmt.Errorf("dirección inválida")
	}
	switch a.Mode {
	case "once", "recurring":
	default:
		return fmt.Errorf("modo inválido")
	}
	switch a.Status {
	case "armed", "paused", "done":
	default:
		return fmt.Errorf("estado inválido")
	}
	if a.RearmBps <= 0 || a.RearmBps > 1000 {
		a.RearmBps = 5
	}
	if a.CooldownSec < 0 || a.CooldownSec > 86400 {
		a.CooldownSec = 300
	}
	if a.MaxPerDay <= 0 || a.MaxPerDay > 1000 {
		a.MaxPerDay = 20
	}
	if len(a.Note) > 200 {
		a.Note = a.Note[:200]
	}
	return nil
}

func (s *Server) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Pool.Exec(r.Context(),
		`DELETE FROM alerts WHERE id=$1 AND symbol=$2`, r.PathValue("id"), s.Symbol); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlertEvents: historial y estado de entrega. Es donde se ve un cruce
// que no llegó a Telegram, que si no sería invisible.
func (s *Server) handleAlertEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), `
		SELECT id, alert_id::text, kind, note, coalesce(direction,''), coalesce(level,0),
		       coalesce(price,0), extract(epoch FROM coalesce(candle_ts, fired_at))::bigint,
		       delivery, detail, attempts
		FROM alert_events WHERE symbol=$1 ORDER BY id DESC LIMIT 100`, s.Symbol)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type ev struct {
		ID        int64   `json:"id"`
		AlertID   *string `json:"alert_id"`
		Kind      string  `json:"kind"`
		Note      string  `json:"note"`
		Direction string  `json:"direction"`
		Level     float64 `json:"level"`
		Price     float64 `json:"price"`
		At        int64   `json:"at"`
		Delivery  string  `json:"delivery"`
		Detail    string  `json:"detail"`
		Attempts  int     `json:"attempts"`
	}
	out := []ev{}
	for rows.Next() {
		var e ev
		var nivel, precio int64
		if err := rows.Scan(&e.ID, &e.AlertID, &e.Kind, &e.Note, &e.Direction, &nivel, &precio,
			&e.At, &e.Delivery, &e.Detail, &e.Attempts); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		e.Level, e.Price = float64(nivel)/e8, float64(precio)/e8
		out = append(out, e)
	}
	writeJSON(w, out)
}

// handleAlertStatus: ¿está vivo el motor y llegaría un aviso? Lo contesta la
// base de datos, sin hablar con el proceso.
func (s *Server) handleAlertStatus(w http.ResponseWriter, r *http.Request) {
	var lastTs *time.Time
	var detalle []byte
	var armadas, pendientes int
	_ = s.Pool.QueryRow(r.Context(),
		`SELECT last_ts, detail FROM alert_engine WHERE symbol=$1`, s.Symbol).Scan(&lastTs, &detalle)
	_ = s.Pool.QueryRow(r.Context(),
		`SELECT count(*) FROM alerts WHERE symbol=$1 AND status='armed'`, s.Symbol).Scan(&armadas)
	_ = s.Pool.QueryRow(r.Context(),
		`SELECT count(*) FROM alert_events WHERE symbol=$1 AND delivery IN ('pending','failed')`,
		s.Symbol).Scan(&pendientes)

	res := map[string]any{"armadas": armadas, "pendientes": pendientes}
	if lastTs != nil {
		res["ultimo_evaluado"] = lastTs.UTC().Format(time.RFC3339)
		res["retraso_seg"] = int64(time.Since(*lastTs).Seconds())
	}
	if len(detalle) > 0 {
		var d map[string]any
		if json.Unmarshal(detalle, &d) == nil {
			res["motor"] = d
		}
	}
	writeJSON(w, res)
}

// handleAlertTest encola un mensaje de prueba. No habla con Telegram: mete la
// fila y el motor la manda, así se prueba el camino ENTERO (incluido que el
// proceso esté vivo) y no solo la API.
func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	var id int64
	if err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO alert_events (kind, symbol, note, delivery)
		VALUES ('test', $1, 'prueba manual desde el panel', 'pending') RETURNING id`,
		s.Symbol).Scan(&id); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}
