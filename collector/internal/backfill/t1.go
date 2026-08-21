package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/fixed"
	"jputellas.dev/btcdash/collector/internal/store"
)

// RunT1 ejecuta la corrección T+1: recalcula las velas 1s de cada día del
// rango desde el fichero de TRADES INDIVIDUALES (futures/um/daily/trades,
// ground truth verificado en F0: las klines oficiales cuadran 1440/1440 con
// él) y las marca quality='exact_t1'. También sobreescribe candles_1m del día
// con las klines oficiales (quality='official', RF-7.2).
//
// Se preservan first/last_agg_id y agg_count de las filas existentes (los
// trades individuales no llevan aggTradeId; esas columnas trazan huecos del
// pipeline y no deben perderse).
//
// Política retroactiva (decidida en F1b): el job avanza SOLO hacia adelante
// por defecto (desde el último día corregido hasta ayer). Aplicarlo a los 2
// años backfilleados costaría ~730 días x ~41 MB = ~30 GB de descarga y horas
// de proceso, para corregir un sesgo de frontera puramente cosmético a nivel
// visual (F0: el volumen diario cuadra al satoshi; solo se desplaza entre
// segundos adyacentes, media 0,29 BTC en los afectados). Si algún día hace
// falta exactitud contable retroactiva, `t1 -from -to` acepta cualquier rango.
func RunT1(ctx context.Context, pg *store.PG, rest *binance.REST, symbol string, from, to time.Time, cacheDir string) error {
	from = from.UTC().Truncate(24 * time.Hour)
	to = to.UTC().Truncate(24 * time.Hour)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		if done, err := pg.JobDone(ctx, "t1", key); err != nil {
			return err
		} else if done {
			slog.Info("t1: day already corrected, skipping", "day", key)
			continue
		}
		if err := t1Day(ctx, pg, rest, symbol, day, cacheDir); err != nil {
			if errors.Is(err, binance.ErrNotPublished) {
				slog.Warn("t1: fichero de trades aún no publicado; se reintentará en la próxima ejecución", "day", key)
				return nil
			}
			return fmt.Errorf("t1 %s: %w", key, err)
		}
	}
	return nil
}

// LastT1Day devuelve el día siguiente al último corregido, o zero si ninguno.
func LastT1Day(ctx context.Context, pg *store.PG) (time.Time, error) {
	var last string
	err := pg.Pool.QueryRow(ctx,
		`SELECT COALESCE(max(key), '') FROM job_progress WHERE job='t1'`).Scan(&last)
	if err != nil || last == "" {
		return time.Time{}, err
	}
	d, err := time.Parse("2006-01-02", last)
	if err != nil {
		return time.Time{}, err
	}
	return d.AddDate(0, 0, 1), nil
}

func t1Day(ctx context.Context, pg *store.PG, rest *binance.REST, symbol string, day time.Time, cacheDir string) error {
	if err := checkDiskFree(cacheDir); err != nil {
		return err
	}
	t0 := time.Now()
	zipPath, err := binance.DownloadVisionZip(ctx, binance.DailyTradesURL(symbol, day), cacheDir)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)

	// Velas 1s exactas desde trades individuales, bucketing por floor(T/1000).
	exact := map[int64]*store.StoredCandle{}
	order := make([]int64, 0, 86400)
	rows, err := binance.StreamTradesZip(zipPath, func(t binance.RawTrade) error {
		sec := t.T / 1000
		c, ok := exact[sec]
		if !ok {
			c = &store.StoredCandle{Symbol: symbol, Quality: store.QualityExactT1}
			c.TsSec, c.Open, c.High, c.Low = sec, t.Price, t.Price, t.Price
			exact[sec] = c
			order = append(order, sec)
		}
		if t.Price > c.High {
			c.High = t.Price
		}
		if t.Price < c.Low {
			c.Low = t.Price
		}
		c.Close = t.Price
		c.Volume += t.Qty
		if !t.IsBuyerMaker {
			c.BuyVolume += t.Qty
		}
		c.TradeCount++
		return nil
	})
	if err != nil {
		return err
	}

	// Diff contra lo existente: la cifra que valida el sesgo de frontera.
	existing, err := pg.LoadDayCandles(ctx, symbol, day)
	if err != nil {
		return err
	}
	var volDiff, ohlcDiff, newSecs int
	var maxAbsDV, sumAbsDV int64
	for _, sec := range order {
		e, ok := existing[sec]
		c := exact[sec]
		if !ok {
			newSecs++
			continue
		}
		if d := abs64(c.Volume - e.Volume); d != 0 {
			volDiff++
			sumAbsDV += d
			if d > maxAbsDV {
				maxAbsDV = d
			}
		}
		if c.Open != e.Open || c.High != e.High || c.Low != e.Low || c.Close != e.Close {
			ohlcDiff++
		}
	}
	orphans := 0 // segundos en BD sin trades reales: no debería pasar (recon ⊆ verdad)
	for sec := range existing {
		if _, ok := exact[sec]; !ok {
			orphans++
		}
	}
	if orphans > 0 {
		slog.Warn("t1: hay velas en BD para segundos SIN trades reales — no se borran, revisar", "day", day.Format("2006-01-02"), "count", orphans)
	}

	// Upsert de TODO el día (también los que no cambian: quedan verificados
	// y marcados exact_t1), en lotes.
	batch := make([]store.StoredCandle, 0, 5000)
	for _, sec := range order {
		batch = append(batch, *exact[sec])
		if len(batch) >= 5000 {
			if err := pg.UpsertExact1s(ctx, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if err := pg.UpsertExact1s(ctx, batch); err != nil {
		return err
	}

	// candles_1m del día con klines oficiales (RF-7.2): bulk diario si existe,
	// REST si no (mismo dato, el endpoint no tiene ventana).
	if err := official1mDay(ctx, pg, rest, symbol, day, cacheDir); err != nil {
		return err
	}

	pctVol := 100 * float64(volDiff) / float64(max(len(order), 1))
	meanDV := int64(0)
	if volDiff > 0 {
		meanDV = sumAbsDV / int64(volDiff)
	}
	detail := fmt.Sprintf(`{"trades":%d,"secs":%d,"vol_diff_secs":%d,"vol_diff_pct":%.2f,"ohlc_diff_secs":%d,"new_secs":%d,"max_abs_dv":"%s","mean_abs_dv":"%s"}`,
		rows, len(order), volDiff, pctVol, ohlcDiff, newSecs, fixed.Format(maxAbsDV), fixed.Format(meanDV))
	if err := pg.MarkJob(ctx, "t1", day.Format("2006-01-02"), detail); err != nil {
		return err
	}
	slog.Info("t1: day corrected", "day", day.Format("2006-01-02"),
		"trades", rows, "secs", len(order),
		"vol_diff_secs", volDiff, "vol_diff_pct", fmt.Sprintf("%.2f%%", pctVol),
		"ohlc_diff_secs", ohlcDiff, "new_secs", newSecs,
		"max_abs_dv", fixed.Format(maxAbsDV), "mean_abs_dv", fixed.Format(meanDV),
		"elapsed", time.Since(t0).Round(time.Second))
	return nil
}

func official1mDay(ctx context.Context, pg *store.PG, rest *binance.REST, symbol string, day time.Time, cacheDir string) error {
	var ks []store.Official1m
	add := func(k binance.Kline1m) error {
		ks = append(ks, store.Official1m{
			TsSec: k.OpenMs / 1000, Open: k.Open, High: k.High, Low: k.Low, Close: k.Close,
			Volume: k.Volume, BuyVolume: k.TakerBuy, TradeCount: k.Count,
		})
		return nil
	}
	url := fmt.Sprintf("https://data.binance.vision/data/futures/um/daily/klines/%s/1m/%s-1m-%s.zip",
		symbol, symbol, day.Format("2006-01-02"))
	zipPath, err := binance.DownloadVisionZip(ctx, url, cacheDir)
	switch {
	case err == nil:
		_, err = binance.StreamKlines1mZip(zipPath, add)
		os.Remove(zipPath)
		if err != nil {
			return err
		}
	case errors.Is(err, binance.ErrNotPublished):
		if _, err := rest.Klines1m(ctx, day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli(), add); err != nil {
			return err
		}
	default:
		return err
	}
	return pg.UpsertOfficial1m(ctx, symbol, ks)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
