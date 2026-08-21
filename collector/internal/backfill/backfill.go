// Package backfill puebla el histórico desde data.binance.vision (R5, F1b).
// Idempotente (upserts + tablas de progreso) y reanudable (los días/meses
// completados se saltan).
package backfill

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/candle"
	"jputellas.dev/btcdash/collector/internal/store"
)

// MinFreeBytes: umbral de disco libre bajo el cual el backfill ABORTA en vez
// de seguir "a ver si cabe". El staging de aggTrades es de hasta ~140 MB/día
// descomprimido y el VPS es compartido: quedarse sin disco tumbaría Hermes.
var MinFreeBytes int64 = 5 << 30 // 5 GiB

// Run rellena velas 1s para [from, to] (días UTC completos, inclusive) desde
// los ficheros diarios de aggTrades. Cada ZIP se procesa y SE BORRA antes de
// pasar al siguiente día: nunca se acumula staging en disco.
//
// Las velas quedan con quality='realtime': el bulk de aggTrades tiene la
// misma fidelidad que el stream (mismo sesgo de frontera, trampa 9);
// 'exact_t1' queda reservado al job T+1 que parte de trades individuales.
func Run(ctx context.Context, pg *store.PG, symbol string, from, to time.Time, cacheDir string) error {
	from = from.UTC().Truncate(24 * time.Hour)
	to = to.UTC().Truncate(24 * time.Hour)
	start := time.Now()
	days := 0
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		if done, err := pg.BackfillDayDone(ctx, symbol, day); err != nil {
			return err
		} else if done {
			slog.Info("backfill: day already done, skipping", "day", day.Format("2006-01-02"))
			continue
		}
		if err := BackfillDay(ctx, pg, symbol, day, cacheDir); err != nil {
			return fmt.Errorf("backfill %s: %w", day.Format("2006-01-02"), err)
		}
		days++
		if days%10 == 0 {
			rate := float64(days) / time.Since(start).Hours()
			slog.Info("backfill: progress", "days_done", days, "days_per_hour", fmt.Sprintf("%.1f", rate))
		}
	}
	end := to.AddDate(0, 0, 1)
	slog.Info("backfill: refreshing CAggs", "from", from, "to", end)
	return pg.RefreshCAggs(ctx, from, end)
}

// BackfillDay procesa un día SIN mirar la tabla de progreso (lo usa también
// resolve-gaps para forzar el reproceso de días con hueco). Marca el progreso
// al terminar y borra el ZIP.
func BackfillDay(ctx context.Context, pg *store.PG, symbol string, day time.Time, cacheDir string) error {
	if err := checkDiskFree(cacheDir); err != nil {
		return err
	}
	t0 := time.Now()
	zipPath, err := binance.DownloadDailyAggTrades(ctx, symbol, day, cacheDir)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath) // procesar y borrar: el staging nunca se acumula

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
	if err := pg.RollupRange(ctx, day, day.AddDate(0, 0, 1)); err != nil {
		return err
	}
	if err := pg.MarkBackfillDay(ctx, symbol, day, rows, candles); err != nil {
		return err
	}
	slog.Info("backfill: day done", "day", day.Format("2006-01-02"),
		"agg_trades", rows, "candles", candles, "elapsed", time.Since(t0).Round(time.Second))
	return nil
}

func checkDiskFree(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}
	free := int64(st.Bavail) * st.Bsize
	if free < MinFreeBytes {
		return fmt.Errorf("DISCO INSUFICIENTE: %.1f GiB libres en %s, umbral %.1f GiB — backfill ABORTADO (reanudable: los días completados no se repiten)",
			float64(free)/(1<<30), dir, float64(MinFreeBytes)/(1<<30))
	}
	return nil
}
