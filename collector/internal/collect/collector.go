// Package collect orquesta el pipeline del colector:
//
//	WS aggTrades ──> detección de huecos ──> Stream 1s ──> buffer ──> TimescaleDB
//	                      │ (aggTradeId)          ▲
//	                      └── reconcile REST ─────┘  (mismo pipeline, en orden)
//
// La reconciliación alimenta los trades recuperados por el MISMO Stream que
// los trades en vivo, en orden de aggTradeId: el segundo frontera entre lo
// reconciliado y lo vivo se fusiona solo, sin lógica de merge en BD.
package collect

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"jputellas.dev/btcdash/collector/internal/candle"
	"jputellas.dev/btcdash/collector/internal/health"
	"jputellas.dev/btcdash/collector/internal/reconcile"
	"jputellas.dev/btcdash/collector/internal/store"
)

// wsRunner y Store son interfaces para poder testear con fakes.
type wsRunner interface {
	Run(ctx context.Context, onTrade func(candle.AggTrade))
}

type Collector struct {
	Symbol          string
	Store           store.Store
	Rest            reconcile.Source
	WS              wsRunner
	Health          *health.State
	ReconcileWindow time.Duration
	BufferSize      int

	// Now es inyectable para los tests de la ventana de reconciliación.
	Now func() time.Time

	writeCh    chan store.StoredCandle
	stream     candle.Stream
	lastAggID  int64 // último aggTradeId procesado por el pipeline
	lastTradeT int64 // T (ms) del último trade procesado
	lastRestID int64 // id más alto alimentado desde REST (marca 'reconciled')
}

// Run bloquea hasta que ctx se cancele; entonces vacía el buffer y retorna.
func (c *Collector) Run(ctx context.Context) error {
	if c.Now == nil {
		c.Now = func() time.Time { return time.Now().UTC() }
	}
	if c.BufferSize == 0 {
		c.BufferSize = 262144
	}
	c.writeCh = make(chan store.StoredCandle, c.BufferSize)
	c.stream.Emit = c.emitClosed

	writerDone := make(chan struct{})
	go c.writer(writerDone)

	tradeCh := make(chan candle.AggTrade, 65536)
	wsCtx, wsCancel := context.WithCancel(ctx)
	defer wsCancel()
	go c.WS.Run(wsCtx, func(t candle.AggTrade) {
		if c.Health != nil {
			c.Health.TradeSeen(t.ID)
		}
		select {
		case tradeCh <- t:
		case <-wsCtx.Done():
		}
	})

	// Bucle principal: único consumidor del pipeline (el orden es sagrado).
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down: flushing in-progress candle and buffer")
			c.stream.Flush() // vela parcial; el rearranque reconstruye ese segundo
			close(c.writeCh)
			<-writerDone
			return nil
		case t := <-tradeCh:
			if err := c.ingest(ctx, t); err != nil {
				if ctx.Err() != nil {
					continue // el apagado sigue su curso en el caso <-ctx.Done()
				}
				return err
			}
		case <-ticker.C:
			// Vuelca la vela EN CURSO sin cerrarla: mantiene la BD fresca en
			// segundos tranquilos y deja la vela actual visible. La versión
			// final la sobreescribirá (upsert idempotente).
			if cur := c.stream.Current(); cur != nil {
				c.push(*cur)
			}
		}
	}
}

// ingest procesa un trade en vivo: dedupe, detección de hueco, agregación.
func (c *Collector) ingest(ctx context.Context, t candle.AggTrade) error {
	if c.lastAggID == 0 {
		// Primer trade tras el arranque: reconcilia desde la última vela en BD.
		if err := c.reconcileStartup(ctx, t); err != nil {
			return err
		}
	} else if t.ID <= c.lastAggID {
		return nil // solapamiento tras reconexión: ya procesado
	} else if t.ID > c.lastAggID+1 {
		// Hueco en vivo (reconexión o pérdida): reconciliar ANTES de este
		// trade para mantener el orden del pipeline.
		if err := c.reconcileGap(ctx, c.lastAggID+1, t.ID-1, t.T); err != nil {
			return err
		}
	}
	return c.feed(t, false)
}

// feed añade un trade al Stream. fromRest marca el origen para `quality`.
func (c *Collector) feed(t candle.AggTrade, fromRest bool) error {
	if fromRest {
		c.lastRestID = t.ID
	}
	if err := c.stream.Add(t); err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	c.lastAggID = t.ID
	c.lastTradeT = t.T
	return nil
}

// emitClosed recibe cada vela cerrada del Stream.
func (c *Collector) emitClosed(cd candle.Candle) {
	c.push(cd)
}

func (c *Collector) push(cd candle.Candle) {
	quality := store.QualityRealtime
	if cd.FirstAggID <= c.lastRestID {
		quality = store.QualityReconciled
	}
	sc := store.StoredCandle{Candle: cd, Symbol: c.Symbol, Quality: quality}
	select {
	case c.writeCh <- sc:
	default:
		// Buffer lleno (caída de BD prolongada). No se pierde en silencio:
		// queda constancia y el hueco lo detectará la reconciliación al
		// rearrancar (RNF-5).
		slog.Error("write buffer full, dropping candle", "ts", cd.TsSec)
	}
}

// reconcileStartup cubre el hueco entre la última vela en BD y el primer
// trade en vivo. Refetch desde first_agg_id de la última vela: así el segundo
// frontera se reconstruye completo aunque el apagado dejara una vela parcial.
func (c *Collector) reconcileStartup(ctx context.Context, first candle.AggTrade) error {
	last, err := c.Store.LastCandle(ctx, c.Symbol)
	if err != nil {
		return fmt.Errorf("startup: %w", err)
	}
	if last == nil {
		slog.Info("empty database: starting fresh from live stream", "first_agg_id", first.ID)
		return nil
	}
	if first.ID <= last.LastAggID {
		return nil // sin hueco: el WS llegó antes de donde íbamos
	}
	return c.runGapMachine(ctx, gapSpec{
		fromID:    last.FirstAggID, // reconstruye el segundo frontera entero
		untilID:   first.ID - 1,
		startTime: time.Unix(last.TsSec, 0).UTC(),
		endTime:   time.UnixMilli(first.T).UTC(),
		reason:    "restart",
	})
}

// reconcileGap cubre un hueco detectado en vivo por discontinuidad de ids.
func (c *Collector) reconcileGap(ctx context.Context, fromID, untilID, nextT int64) error {
	return c.runGapMachine(ctx, gapSpec{
		fromID:    fromID,
		untilID:   untilID,
		startTime: time.UnixMilli(c.lastTradeT).UTC(),
		endTime:   time.UnixMilli(nextT).UTC(),
		reason:    "stream discontinuity",
	})
}

type gapSpec struct {
	fromID, untilID    int64
	startTime, endTime time.Time
	reason             string
}

// runGapMachine ejecuta la máquina de estados de un hueco (R4).
func (c *Collector) runGapMachine(ctx context.Context, g gapSpec) error {
	now := c.Now()
	plan := reconcile.Classify(g.startTime, g.endTime, now, c.ReconcileWindow)
	slog.Warn("gap detected", "from_id", g.fromID, "until_id", g.untilID,
		"start", g.startTime, "end", g.endTime, "reason", g.reason,
		"rest_recoverable", plan.Rest != nil, "pending_bulk", plan.Bulk != nil)

	if plan.Bulk != nil {
		// Irrecuperable por REST: registrar y ALERTAR. Lo resuelve F1b.
		id, err := c.Store.InsertGap(ctx, store.Gap{
			Symbol: c.Symbol, Start: plan.Bulk.Start, End: plan.Bulk.End,
			FirstMissingID: g.fromID, Status: store.GapPendingBulk,
			Reason: g.reason + ": older than reconcile window, needs bulk backfill",
		})
		if err != nil {
			return fmt.Errorf("gap machine: record pending_bulk: %w", err)
		}
		slog.Error("ALERT: gap NOT recoverable via REST, marked pending_bulk",
			"gap_id", id, "start", plan.Bulk.Start, "end", plan.Bulk.End)
	}
	if plan.Rest == nil {
		return nil
	}

	gapID, err := c.Store.InsertGap(ctx, store.Gap{
		Symbol: c.Symbol, Start: plan.Rest.Start, End: plan.Rest.End,
		FirstMissingID: g.fromID, LastMissingID: g.untilID,
		Status: store.GapReconciling, Reason: g.reason,
	})
	if err != nil {
		return fmt.Errorf("gap machine: record: %w", err)
	}

	feed := func(t candle.AggTrade) error {
		if t.ID <= c.lastAggID && c.lastAggID > 0 {
			return nil
		}
		return c.feed(t, true)
	}
	if plan.Bulk != nil {
		// Tramo tras un pending_bulk: no hay id de siembra dentro de la
		// ventana; sembrar por startTime y seguir por fromId.
		err = reconcile.FetchSince(ctx, c.Rest, plan.Rest.Start.UnixMilli(), g.untilID, feed)
	} else {
		err = reconcile.FetchByID(ctx, c.Rest, g.fromID, g.untilID, feed)
	}
	if err != nil {
		// El hueco queda 'open' con su causa: NUNCA se cierra en silencio.
		if uerr := c.Store.UpdateGapStatus(ctx, gapID, store.GapOpen,
			fmt.Sprintf("reconcile failed: %v", err)); uerr != nil {
			slog.Error("gap machine: update failed", "err", uerr)
		}
		return fmt.Errorf("gap machine: fetch: %w", err)
	}
	if err := c.Store.UpdateGapStatus(ctx, gapID, store.GapResolved, ""); err != nil {
		return err
	}
	slog.Info("gap reconciled via REST", "gap_id", gapID, "from_id", g.fromID, "until_id", g.untilID)
	return nil
}

// writer consume velas y las persiste en lotes, con reintentos.
func (c *Collector) writer(done chan struct{}) {
	defer close(done)
	pending := make(map[int64]store.StoredCandle) // por segundo: la última versión gana
	var order []int64
	flush := func(ctx context.Context) {
		if len(pending) == 0 {
			return
		}
		batch := make([]store.StoredCandle, 0, len(order))
		for _, sec := range order {
			if sc, ok := pending[sec]; ok {
				batch = append(batch, sc)
				delete(pending, sec)
			}
		}
		order = order[:0]
		for {
			err := c.Store.UpsertCandles(ctx, batch)
			if err == nil {
				if c.Health != nil {
					c.Health.CandleWritten(batch[len(batch)-1].TsSec)
				}
				return
			}
			slog.Error("db write failed, retrying", "err", err, "batch", len(batch))
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				slog.Error("db write abandoned on shutdown", "unwritten", len(batch))
				return
			}
		}
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case sc, ok := <-c.writeCh:
			if !ok {
				// Apagado: último intento de vaciar con un margen acotado.
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				flush(ctx)
				cancel()
				return
			}
			if _, dup := pending[sc.TsSec]; !dup {
				order = append(order, sc.TsSec)
			}
			pending[sc.TsSec] = sc
			if len(pending) >= 2000 {
				flush(context.Background())
			}
		case <-ticker.C:
			flush(context.Background())
		}
	}
}

// BufferLen expone la ocupación del buffer para /health.
func (c *Collector) BufferLen() int {
	if c.writeCh == nil {
		return 0
	}
	return len(c.writeCh)
}
