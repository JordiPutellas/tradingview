package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"jputellas.dev/btcdash/collector/internal/store"
)

// Motor es el proceso que evalúa alertas. Solo LEE candles_1s y escribe en sus
// tres tablas; no toca nada del colector.
type Motor struct {
	PG         *store.PG
	Symbol     string
	TG         *Telegram
	ReplayMax  time.Duration // hueco máximo que se reproduce al arrancar
	Vigilancia time.Duration // sin avisos, cada cuánto mirar por si acaso
}

func (m *Motor) defaults() {
	if m.ReplayMax == 0 {
		m.ReplayMax = 2 * time.Hour
	}
	if m.Vigilancia == 0 {
		m.Vigilancia = 15 * time.Second
	}
}

// Run escucha NOTIFY 'candle_update' —el mismo que ya emite el colector para
// el WebSocket— y evalúa. El NOTIFY es para la latencia; la verdad está en la
// tabla, porque el colector solo publica la vela MÁS RECIENTE de cada lote y
// tras un reintento de base de datos un lote puede traer varios segundos.
// Por eso cada pasada lee el tramo entero desde la marca de agua.
func (m *Motor) Run(ctx context.Context) error {
	m.defaults()
	// Si la migración aún no ha corrido (el colector es quien migra), esto
	// falla: no es motivo para morirse, la siguiente pasada lo reintenta.
	if err := m.sembrarMarca(ctx); err != nil {
		slog.Warn("marca de agua todavía no disponible, reintento en marcha", "err", err)
	}
	slog.Info("motor de alertas en marcha", "symbol", m.Symbol,
		"telegram", m.TG.Configurado(), "replay_max", m.ReplayMax.String())

	avisos := make(chan struct{}, 1)
	go m.escuchar(ctx, avisos)

	tick := time.NewTicker(m.Vigilancia)
	defer tick.Stop()
	for {
		if err := m.Paso(ctx); err != nil && ctx.Err() == nil {
			slog.Error("paso de alertas", "err", err)
		}
		if _, err := m.Drenar(ctx); err != nil && ctx.Err() == nil {
			slog.Error("envío de alertas", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-avisos:
		case <-tick.C: // vigilancia: si el LISTEN se muere en silencio, seguimos
		}
	}
}

// escuchar reenvía cada NOTIFY como un aviso sin bloquear: si el canal está
// lleno es que ya hay una pasada pendiente.
func (m *Motor) escuchar(ctx context.Context, avisos chan<- struct{}) {
	for ctx.Err() == nil {
		err := func() error {
			conn, err := m.PG.Pool.Acquire(ctx)
			if err != nil {
				return err
			}
			defer conn.Release()
			if _, err := conn.Exec(ctx, "LISTEN candle_update"); err != nil {
				return err
			}
			for {
				if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
					return err
				}
				select {
				case avisos <- struct{}{}:
				default:
				}
			}
		}()
		if err != nil && ctx.Err() == nil {
			slog.Warn("listen de alertas caído, reintentando", "err", err)
			time.Sleep(2 * time.Second)
		}
	}
}

func (m *Motor) sembrarMarca(ctx context.Context) error {
	_, err := m.PG.Pool.Exec(ctx, `
		INSERT INTO alert_engine (symbol, last_ts)
		VALUES ($1, (SELECT coalesce(max(ts), now()) FROM candles_1s WHERE symbol=$1))
		ON CONFLICT (symbol) DO NOTHING`, m.Symbol)
	return err
}

// Paso evalúa todo lo que haya entre la marca de agua y el último segundo
// CERRADO, y confirma estado, eventos y marca en una sola transacción.
func (m *Motor) Paso(ctx context.Context) error {
	m.defaults()
	var marca time.Time
	if err := m.PG.Pool.QueryRow(ctx,
		`SELECT last_ts FROM alert_engine WHERE symbol=$1`, m.Symbol).Scan(&marca); err != nil {
		// Primera vez con este símbolo (o la migración acaba de correr): se
		// siembra la marca en el presente y se empieza a mirar desde ahí. Nunca
		// hacia atrás: nadie quiere que al estrenar el motor le lleguen los
		// cruces de la semana pasada.
		if err := m.sembrarMarca(ctx); err != nil {
			return fmt.Errorf("sembrar marca: %w", err)
		}
		if err := m.PG.Pool.QueryRow(ctx,
			`SELECT last_ts FROM alert_engine WHERE symbol=$1`, m.Symbol).Scan(&marca); err != nil {
			return fmt.Errorf("marca: %w", err)
		}
	}
	var ultimo *time.Time
	if err := m.PG.Pool.QueryRow(ctx,
		`SELECT max(ts) FROM candles_1s WHERE symbol=$1`, m.Symbol).Scan(&ultimo); err != nil {
		return fmt.Errorf("última vela: %w", err)
	}
	if ultimo == nil {
		return nil
	}

	// Hueco largo: avisar de un cruce de hace tres días es peor que no avisar.
	// Se salta, se deja constancia y se sigue desde ahora.
	if ultimo.Sub(marca) > m.ReplayMax {
		saltado := ultimo.Sub(marca)
		nueva := ultimo.Add(-time.Minute)
		if _, err := m.PG.Pool.Exec(ctx, `
			INSERT INTO alert_events (kind, symbol, note, delivery, detail, fired_at)
			VALUES ('system', $1, $2, 'skipped', 'replay_max', now())`,
			m.Symbol, fmt.Sprintf("motor parado %s: no se reproducen los cruces de ese hueco",
				saltado.Truncate(time.Minute))); err != nil {
			return err
		}
		if _, err := m.PG.Pool.Exec(ctx,
			`UPDATE alert_engine SET last_ts=$2, updated_at=now() WHERE symbol=$1`, m.Symbol, nueva); err != nil {
			return err
		}
		slog.Warn("hueco largo del motor: no se reproduce", "hueco", saltado.String())
		marca = nueva
	}

	desde, hasta, hay := Ventana(marca, *ultimo)
	if !hay {
		return nil
	}

	alertas, err := m.cargar(ctx)
	if err != nil {
		return err
	}
	velas, err := m.velas(ctx, desde, hasta)
	if err != nil {
		return err
	}
	if len(velas) == 0 {
		// Tramo sin datos (el colector estuvo parado): la marca avanza igual o
		// nos quedaríamos mirando un hueco para siempre.
		_, err := m.PG.Pool.Exec(ctx,
			`UPDATE alert_engine SET last_ts=$2, updated_at=now() WHERE symbol=$1`, m.Symbol, hasta)
		return err
	}

	ahora := time.Now().UTC()
	type cambio struct {
		a        Alerta
		disparos []*Disparo
	}
	cambios := map[string]*cambio{}
	for i := range alertas {
		a := alertas[i]
		c := &cambio{a: a}
		for _, v := range velas {
			lado, d := Evaluar(c.a, v, ahora)
			c.a.Side = lado
			if d == nil {
				continue
			}
			c.disparos = append(c.disparos, d)
			if d.Entregar {
				c.a.FiredCount++
				t := ahora
				c.a.LastFiredAt = &t
			}
			if d.Pausar {
				c.a.Status = "paused"
			}
			if d.Terminar {
				c.a.Status = "done"
				break
			}
		}
		if c.a.Side != a.Side || len(c.disparos) > 0 {
			cambios[a.ID] = c
		}
	}

	ultimaVela := velas[len(velas)-1]
	return pgx.BeginFunc(ctx, m.PG.Pool, func(tx pgx.Tx) error {
		for _, c := range cambios {
			if _, err := tx.Exec(ctx, `
				UPDATE alerts SET side=$2, fired_count=$3, last_fired_at=$4, status=$5, updated_at=now()
				WHERE id=$1`, c.a.ID, c.a.Side, c.a.FiredCount, c.a.LastFiredAt, c.a.Status); err != nil {
				return err
			}
			for _, d := range c.disparos {
				entrega, detalle := "pending", ""
				if !d.Entregar {
					entrega, detalle = "skipped", d.Motivo
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO alert_events
					  (alert_id, kind, symbol, note, direction, level, price, candle_ts, delivery, detail)
					VALUES ($1,'cross',$2,$3,$4,$5,$6,$7,$8,$9)`,
					c.a.ID, c.a.Symbol, c.a.Note, d.Direccion, c.a.Level, d.Precio, d.Vela,
					entrega, detalle); err != nil {
					return err
				}
			}
		}
		detalle, _ := json.Marshal(map[string]any{
			"telegram": m.TG.Configurado(), "velas": len(velas), "alertas": len(alertas),
		})
		_, err := tx.Exec(ctx, `
			UPDATE alert_engine SET last_ts=$2, last_price=$3, detail=$4, updated_at=now()
			WHERE symbol=$1`, m.Symbol, ultimaVela.Ts, ultimaVela.Close, detalle)
		return err
	})
}

func (m *Motor) cargar(ctx context.Context) ([]Alerta, error) {
	// fired_count solo cuenta los de HOY: el tope diario se reinicia solo.
	rows, err := m.PG.Pool.Query(ctx, `
		SELECT id::text, symbol, level, direction, mode, status, note,
		       rearm_bps, cooldown_sec, max_per_day,
		       coalesce(side, $2),
		       CASE WHEN last_fired_at >= date_trunc('day', now() AT TIME ZONE 'UTC')
		            THEN fired_count ELSE 0 END,
		       last_fired_at
		FROM alerts WHERE symbol=$1 AND status='armed' ORDER BY created_at`, m.Symbol, SinLado)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alerta
	for rows.Next() {
		var a Alerta
		if err := rows.Scan(&a.ID, &a.Symbol, &a.Level, &a.Direction, &a.Mode, &a.Status, &a.Note,
			&a.RearmBps, &a.CooldownSec, &a.MaxPerDay, &a.Side, &a.FiredCount, &a.LastFiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *Motor) velas(ctx context.Context, desde, hasta time.Time) ([]Vela, error) {
	rows, err := m.PG.Pool.Query(ctx, `
		SELECT ts, high, low, close FROM candles_1s
		WHERE symbol=$1 AND ts >= $2 AND ts <= $3 ORDER BY ts`, m.Symbol, desde, hasta)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vela
	for rows.Next() {
		var v Vela
		if err := rows.Scan(&v.Ts, &v.High, &v.Low, &v.Close); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Drenar manda lo que haya en la cola de salida. Los eventos se escriben ANTES
// de hablar con Telegram: si el proceso muere entre el disparo y el envío, el
// mensaje sigue pendiente y sale al arrancar.
func (m *Motor) Drenar(ctx context.Context) (int, error) {
	m.defaults()
	rows, err := m.PG.Pool.Query(ctx, `
		SELECT id, symbol, note, coalesce(direction,''), coalesce(level,0), coalesce(price,0),
		       coalesce(candle_ts, fired_at), kind
		FROM alert_events
		WHERE delivery IN ('pending','failed') AND attempts < 5
		ORDER BY id LIMIT 20`)
	if err != nil {
		return 0, err
	}
	type ev struct {
		id                int64
		symbol, nota, dir string
		nivel, precio     int64
		cuando            time.Time
		kind              string
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.id, &e.symbol, &e.nota, &e.dir, &e.nivel, &e.precio, &e.cuando, &e.kind); err != nil {
			rows.Close()
			return 0, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	enviados := 0
	for _, e := range evs {
		if !m.TG.Configurado() {
			// Sin bot no se acumulan mensajes viejos para soltarlos de golpe el
			// día que se configure: quedan marcados y visibles en el panel.
			if _, err := m.PG.Pool.Exec(ctx, `
				UPDATE alert_events SET delivery='skipped', detail='telegram sin configurar'
				WHERE id=$1`, e.id); err != nil {
				return enviados, err
			}
			continue
		}
		texto := Mensaje(e.symbol, e.nivel, e.precio, e.dir, e.cuando, e.nota)
		if e.kind == "test" {
			texto = "🔧 prueba del sistema de alertas de " + e.symbol
		}
		err := m.TG.Enviar(ctx, texto)
		if err != nil {
			if _, err2 := m.PG.Pool.Exec(ctx, `
				UPDATE alert_events SET delivery='failed', attempts=attempts+1, detail=$2
				WHERE id=$1`, e.id, err.Error()); err2 != nil {
				return enviados, err2
			}
			slog.Warn("telegram falló", "evento", e.id, "err", err)
			continue
		}
		if _, err := m.PG.Pool.Exec(ctx, `
			UPDATE alert_events SET delivery='sent', attempts=attempts+1, sent_at=now(), detail=''
			WHERE id=$1`, e.id); err != nil {
			return enviados, err
		}
		enviados++
	}
	return enviados, nil
}
