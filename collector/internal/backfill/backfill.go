// Package backfill puebla el histórico desde data.binance.vision (R5).
// Idempotente (upserts + tabla de progreso) y reanudable (días completados se
// saltan; los ZIP verificados se cachean en disco).
package backfill

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/candle"
	"jputellas.dev/btcdash/collector/internal/store"
)

// Run rellena [from, to] (días UTC completos, inclusive). Las velas quedan
// con quality='realtime': el bulk de aggTrades tiene la misma fidelidad que
// el stream (mismo sesgo de frontera, trampa 9); 'exact_t1' queda reservado
// al job T+1 de F1b que parte de trades individuales.
func Run(ctx context.Context, pg *store.PG, symbol string, from, to time.Time, cacheDir string) error {
	from = from.UTC().Truncate(24 * time.Hour)
	to = to.UTC().Truncate(24 * time.Hour)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		if done, err := pg.BackfillDayDone(ctx, symbol, day); err != nil {
			return err
		} else if done {
			slog.Info("backfill: day already done, skipping", "day", day.Format("2006-01-02"))
			continue
		}
		if err := backfillDay(ctx, pg, symbol, day, cacheDir); err != nil {
			return fmt.Errorf("backfill %s: %w", day.Format("2006-01-02"), err)
		}
	}
	end := to.AddDate(0, 0, 1)
	slog.Info("backfill: rolling up candles_1m and refreshing CAggs", "from", from, "to", end)
	if err := pg.RollupRange(ctx, from, end); err != nil {
		return err
	}
	return pg.RefreshCAggs(ctx, from, end)
}

func backfillDay(ctx context.Context, pg *store.PG, symbol string, day time.Time, cacheDir string) error {
	t0 := time.Now()
	zipPath, err := binance.DownloadDailyAggTrades(ctx, symbol, day, cacheDir)
	if err != nil {
		return err
	}
	var batch []store.StoredCandle
	var candles int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := pg.UpsertCandles(ctx, batch); err != nil {
			return err
		}
		candles += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	stream := candle.Stream{Emit: func(c candle.Candle) {
		batch = append(batch, store.StoredCandle{Candle: c, Symbol: symbol, Quality: store.QualityRealtime})
	}}
	rows, err := binance.StreamAggTradesZip(zipPath, func(t candle.AggTrade) error {
		if err := stream.Add(t); err != nil {
			return err
		}
		if len(batch) >= 5000 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	stream.Flush()
	if err := flush(); err != nil {
		return err
	}
	if err := pg.MarkBackfillDay(ctx, symbol, day, rows, candles); err != nil {
		return err
	}
	slog.Info("backfill: day done", "day", day.Format("2006-01-02"),
		"agg_trades", rows, "candles", candles, "elapsed", time.Since(t0).Round(time.Second))
	return nil
}
