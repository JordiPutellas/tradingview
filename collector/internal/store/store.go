// Package store persiste velas y huecos en TimescaleDB.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"jputellas.dev/btcdash/collector/internal/candle"
)

// Quality de una vela (ver README, sección 6).
const (
	QualityRealtime   = "realtime"
	QualityReconciled = "reconciled"
	QualityExactT1    = "exact_t1"
)

// Estados de un hueco en data_gaps.
const (
	GapOpen        = "open"
	GapReconciling = "reconciling"
	GapResolved    = "resolved"
	GapPendingBulk = "pending_bulk"
)

// StoredCandle es una vela de 1s lista para persistir.
type StoredCandle struct {
	candle.Candle
	Symbol  string
	Quality string
}

// Gap es un hueco detectado en la secuencia de aggTradeIds.
type Gap struct {
	ID             int64
	Symbol         string
	Start, End     time.Time
	FirstMissingID int64
	LastMissingID  int64
	Status         string
	Reason         string
}

// Store es la interfaz que usan colector, reconciliador y backfill.
// La implementación real es PG; los tests usan un fake en memoria.
type Store interface {
	UpsertCandles(ctx context.Context, cs []StoredCandle) error
	LastCandle(ctx context.Context, symbol string) (*StoredCandle, error)
	InsertGap(ctx context.Context, g Gap) (int64, error)
	UpdateGapStatus(ctx context.Context, id int64, status, reason string) error
	OpenGapCount(ctx context.Context, symbol string) (int, error)
}

// PG implementa Store sobre pgxpool.
type PG struct {
	Pool *pgxpool.Pool
}

func OpenPG(ctx context.Context, url string) (*PG, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PG{Pool: pool}, nil
}

const upsertSQL = `INSERT INTO candles_1s
  (ts, symbol, open, high, low, close, volume, buy_volume, trade_count, agg_count, first_agg_id, last_agg_id, quality)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (symbol, ts) DO UPDATE SET
  open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close,
  volume=EXCLUDED.volume, buy_volume=EXCLUDED.buy_volume,
  trade_count=EXCLUDED.trade_count, agg_count=EXCLUDED.agg_count,
  first_agg_id=EXCLUDED.first_agg_id, last_agg_id=EXCLUDED.last_agg_id,
  quality=EXCLUDED.quality`

func (s *PG) UpsertCandles(ctx context.Context, cs []StoredCandle) error {
	if len(cs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range cs {
		batch.Queue(upsertSQL,
			time.Unix(c.TsSec, 0).UTC(), c.Symbol,
			c.Open, c.High, c.Low, c.Close, c.Volume, c.BuyVolume,
			c.TradeCount, c.AggCount, c.FirstAggID, c.LastAggID, c.Quality)
	}
	br := s.Pool.SendBatch(ctx, batch)
	defer br.Close()
	for range cs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert candles: %w", err)
		}
	}
	return nil
}

func (s *PG) LastCandle(ctx context.Context, symbol string) (*StoredCandle, error) {
	var c StoredCandle
	var ts time.Time
	err := s.Pool.QueryRow(ctx, `SELECT ts, open, high, low, close, volume, buy_volume,
	    trade_count, agg_count, first_agg_id, last_agg_id, quality
	  FROM candles_1s WHERE symbol=$1 ORDER BY ts DESC LIMIT 1`, symbol).
		Scan(&ts, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &c.BuyVolume,
			&c.TradeCount, &c.AggCount, &c.FirstAggID, &c.LastAggID, &c.Quality)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.TsSec = ts.Unix()
	c.Symbol = symbol
	return &c, nil
}

func (s *PG) InsertGap(ctx context.Context, g Gap) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `INSERT INTO data_gaps
	    (symbol, gap_start, gap_end, first_missing_agg_id, last_missing_agg_id, status, reason)
	  VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		g.Symbol, g.Start, g.End, g.FirstMissingID, g.LastMissingID, g.Status, g.Reason).Scan(&id)
	return id, err
}

func (s *PG) UpdateGapStatus(ctx context.Context, id int64, status, reason string) error {
	var resolvedAt any
	if status == GapResolved {
		resolvedAt = time.Now().UTC()
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE data_gaps SET status=$2, reason=COALESCE(NULLIF($3,''), reason), resolved_at=COALESCE($4, resolved_at) WHERE id=$1`,
		id, status, reason, resolvedAt)
	return err
}

func (s *PG) OpenGapCount(ctx context.Context, symbol string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM data_gaps WHERE symbol=$1 AND status IN ('open','reconciling','pending_bulk')`,
		symbol).Scan(&n)
	return n, err
}

// MarkBackfillDay registra un día completado del backfill (idempotente).
func (s *PG) MarkBackfillDay(ctx context.Context, symbol string, day time.Time, rows, candles int64) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO backfill_progress (symbol, day, rows_ingested, candles)
	  VALUES ($1,$2,$3,$4) ON CONFLICT (symbol, day) DO UPDATE
	  SET rows_ingested=EXCLUDED.rows_ingested, candles=EXCLUDED.candles, completed_at=now()`,
		symbol, day.Format("2006-01-02"), rows, candles)
	return err
}

func (s *PG) BackfillDayDone(ctx context.Context, symbol string, day time.Time) (bool, error) {
	var done bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM backfill_progress WHERE symbol=$1 AND day=$2)`,
		symbol, day.Format("2006-01-02")).Scan(&done)
	return done, err
}

// RefreshCAggs refresca los continuous aggregates sobre un rango (tras un
// backfill). Cada CALL va en autocommit: no puede ir en transacción.
func (s *PG) RefreshCAggs(ctx context.Context, from, to time.Time) error {
	for _, view := range []string{"candles_1m", "candles_5m", "candles_15m", "candles_1h", "candles_4h", "candles_1d"} {
		if _, err := s.Pool.Exec(ctx,
			fmt.Sprintf(`CALL refresh_continuous_aggregate('%s', $1, $2)`, view), from, to); err != nil {
			return fmt.Errorf("refresh %s: %w", view, err)
		}
	}
	return nil
}
