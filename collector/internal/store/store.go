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
	// RollupRange recalcula candles_1m desde candles_1s para [from, to).
	RollupRange(ctx context.Context, from, to time.Time) error
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

// RollupRange recalcula candles_1m (tabla real) desde candles_1s.
func (s *PG) RollupRange(ctx context.Context, from, to time.Time) error {
	if _, err := s.Pool.Exec(ctx, `SELECT rollup_candles_1m($1, $2)`, from, to); err != nil {
		return fmt.Errorf("rollup candles_1m: %w", err)
	}
	return nil
}

// JobDone / MarkJob: progreso genérico de jobs batch (F1b).
func (s *PG) JobDone(ctx context.Context, job, key string) (bool, error) {
	var done bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM job_progress WHERE job=$1 AND key=$2)`, job, key).Scan(&done)
	return done, err
}

func (s *PG) MarkJob(ctx context.Context, job, key, detailJSON string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO job_progress (job, key, detail)
	  VALUES ($1,$2,$3::jsonb) ON CONFLICT (job, key) DO UPDATE
	  SET detail=EXCLUDED.detail, completed_at=now()`, job, key, detailJSON)
	return err
}

// ListGapsByStatus devuelve los huecos de un estado (para resolve-gaps).
func (s *PG) ListGapsByStatus(ctx context.Context, symbol, status string) ([]Gap, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, gap_start, gap_end, status, COALESCE(reason,'')
	  FROM data_gaps WHERE symbol=$1 AND status=$2 ORDER BY id`, symbol, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Gap
	for rows.Next() {
		g := Gap{Symbol: symbol}
		if err := rows.Scan(&g.ID, &g.Start, &g.End, &g.Status, &g.Reason); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// LoadDayCandles carga las velas 1s de un día UTC (para el diff del job T+1).
func (s *PG) LoadDayCandles(ctx context.Context, symbol string, day time.Time) (map[int64]StoredCandle, error) {
	rows, err := s.Pool.Query(ctx, `SELECT ts, open, high, low, close, volume, buy_volume,
	    trade_count, agg_count, first_agg_id, last_agg_id, quality
	  FROM candles_1s WHERE symbol=$1 AND ts >= $2 AND ts < $3`,
		symbol, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]StoredCandle{}
	for rows.Next() {
		var c StoredCandle
		var ts time.Time
		if err := rows.Scan(&ts, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &c.BuyVolume,
			&c.TradeCount, &c.AggCount, &c.FirstAggID, &c.LastAggID, &c.Quality); err != nil {
			return nil, err
		}
		c.TsSec = ts.Unix()
		c.Symbol = symbol
		out[c.TsSec] = c
	}
	return out, rows.Err()
}

// UpsertExact1s escribe velas 1s exactas del job T+1. En filas existentes
// PRESERVA first/last_agg_id y agg_count (los trades individuales no llevan
// aggTradeId y esas columnas sirven para trazar huecos del pipeline).
const upsertExactSQL = `INSERT INTO candles_1s
  (ts, symbol, open, high, low, close, volume, buy_volume, trade_count, agg_count, first_agg_id, last_agg_id, quality)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,0,0,'exact_t1')
ON CONFLICT (symbol, ts) DO UPDATE SET
  open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close,
  volume=EXCLUDED.volume, buy_volume=EXCLUDED.buy_volume,
  trade_count=EXCLUDED.trade_count, quality='exact_t1'`

func (s *PG) UpsertExact1s(ctx context.Context, cs []StoredCandle) error {
	if len(cs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range cs {
		batch.Queue(upsertExactSQL, time.Unix(c.TsSec, 0).UTC(), c.Symbol,
			c.Open, c.High, c.Low, c.Close, c.Volume, c.BuyVolume, c.TradeCount)
	}
	br := s.Pool.SendBatch(ctx, batch)
	defer br.Close()
	for range cs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert exact 1s: %w", err)
		}
	}
	return nil
}

// Official1m es una kline 1m oficial lista para candles_1m.
type Official1m struct {
	TsSec      int64
	Open       int64
	High       int64
	Low        int64
	Close      int64
	Volume     int64
	BuyVolume  int64
	TradeCount int64
}

// UpsertOfficial1m escribe klines oficiales en candles_1m (quality='official',
// que el rollup desde 1s nunca pisa). Las klines no llevan aggTradeIds: esas
// columnas quedan a 0 en filas oficiales (documentado en README).
func (s *PG) UpsertOfficial1m(ctx context.Context, symbol string, ks []Official1m) error {
	if len(ks) == 0 {
		return nil
	}
	const sql = `INSERT INTO candles_1m
	  (ts, symbol, open, high, low, close, volume, buy_volume, trade_count, agg_count, first_agg_id, last_agg_id, quality)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,0,0,'official')
	ON CONFLICT (symbol, ts) DO UPDATE SET
	  open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close,
	  volume=EXCLUDED.volume, buy_volume=EXCLUDED.buy_volume,
	  trade_count=EXCLUDED.trade_count, quality='official'`
	batch := &pgx.Batch{}
	for _, k := range ks {
		batch.Queue(sql, time.Unix(k.TsSec, 0).UTC(), symbol,
			k.Open, k.High, k.Low, k.Close, k.Volume, k.BuyVolume, k.TradeCount)
	}
	br := s.Pool.SendBatch(ctx, batch)
	defer br.Close()
	for range ks {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert official 1m: %w", err)
		}
	}
	return nil
}

// RefreshCAggs refresca los continuous aggregates >=5m sobre un rango (tras
// un backfill). Cada CALL va en autocommit: no puede ir en transacción.
func (s *PG) RefreshCAggs(ctx context.Context, from, to time.Time) error {
	for _, view := range []string{"candles_5m", "candles_15m", "candles_1h", "candles_4h", "candles_1d"} {
		if _, err := s.Pool.Exec(ctx,
			fmt.Sprintf(`CALL refresh_continuous_aggregate('%s', $1, $2)`, view), from, to); err != nil {
			return fmt.Errorf("refresh %s: %w", view, err)
		}
	}
	return nil
}
