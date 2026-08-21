package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/store"
)

// Run1m puebla candles_1m con las klines OFICIALES de futures desde `from`
// hasta ahora (quality='official'; el rollup desde 1s nunca las pisa).
//
// Fuentes, por prioridad:
//   - Meses completos: ZIP mensual de data.binance.vision (existe desde
//     2020-01; con CHECKSUM). Progreso por mes en job_progress('klines1m').
//   - Meses sin ZIP (2019, el par nace el 2019-09-08) y el mes en curso:
//     REST /fapi/v1/klines, que no tiene ventana de 48 h y llega al origen.
//     El mes en curso se repuebla en cada ejecución (idempotente, <=31 días).
func Run1m(ctx context.Context, pg *store.PG, rest *binance.REST, symbol string, from time.Time, cacheDir string) error {
	from = from.UTC()
	now := time.Now().UTC()
	curMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var total int64

	var batch []store.Official1m
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := pg.UpsertOfficial1m(ctx, symbol, batch); err != nil {
			return err
		}
		total += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	add := func(k binance.Kline1m) error {
		batch = append(batch, store.Official1m{
			TsSec: k.OpenMs / 1000, Open: k.Open, High: k.High, Low: k.Low, Close: k.Close,
			Volume: k.Volume, BuyVolume: k.TakerBuy, TradeCount: k.Count,
		})
		if len(batch) >= 2000 {
			return flush()
		}
		return nil
	}

	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(curMonth); m = m.AddDate(0, 1, 0) {
		next := m.AddDate(0, 1, 0)
		key := m.Format("2006-01")
		if m.Equal(curMonth) {
			// Mes en curso: siempre por REST hasta el último minuto cerrado.
			endMs := now.Truncate(time.Minute).UnixMilli()
			n, err := rest.Klines1m(ctx, m.UnixMilli(), endMs, add)
			if err != nil {
				return fmt.Errorf("klines1m REST %s: %w", key, err)
			}
			if err := flush(); err != nil {
				return err
			}
			slog.Info("klines1m: current month via REST", "month", key, "klines", n)
			continue
		}
		if done, err := pg.JobDone(ctx, "klines1m", key); err != nil {
			return err
		} else if done {
			continue
		}
		t0 := time.Now()
		zipPath, err := binance.DownloadVisionZip(ctx, binance.MonthlyKlines1mURL(symbol, m), cacheDir)
		var n int64
		switch {
		case err == nil:
			n, err = binance.StreamKlines1mZip(zipPath, add)
			os.Remove(zipPath)
			if err != nil {
				return fmt.Errorf("klines1m zip %s: %w", key, err)
			}
		case errors.Is(err, binance.ErrNotPublished):
			// Mes sin ZIP (2019): REST hasta el origen del par.
			n, err = rest.Klines1m(ctx, m.UnixMilli(), next.UnixMilli(), add)
			if err != nil {
				return fmt.Errorf("klines1m REST %s: %w", key, err)
			}
			slog.Info("klines1m: month has no bulk file, used REST", "month", key)
		default:
			return fmt.Errorf("klines1m %s: %w", key, err)
		}
		if err := flush(); err != nil {
			return err
		}
		if err := pg.MarkJob(ctx, "klines1m", key, fmt.Sprintf(`{"klines":%d}`, n)); err != nil {
			return err
		}
		slog.Info("klines1m: month done", "month", key, "klines", n, "elapsed", time.Since(t0).Round(time.Second))
	}
	slog.Info("klines1m: refreshing CAggs", "from", from, "upserted", total)
	return pg.RefreshCAggs(ctx, from, now)
}
