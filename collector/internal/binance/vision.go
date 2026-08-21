package binance

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"jputellas.dev/btcdash/collector/internal/fixed"
)

// ErrNotPublished: el fichero aún no existe en data.binance.vision (404).
var ErrNotPublished = errors.New("data.binance.vision: fichero no publicado (404)")

const visionBase = "https://data.binance.vision/data/futures/um"

func MonthlyKlines1mURL(symbol string, month time.Time) string {
	return fmt.Sprintf("%s/monthly/klines/%s/1m/%s-1m-%s.zip", visionBase, symbol, symbol, month.Format("2006-01"))
}

func DailyTradesURL(symbol string, day time.Time) string {
	return fmt.Sprintf("%s/daily/trades/%s/%s-trades-%s.zip", visionBase, symbol, symbol, day.Format("2006-01-02"))
}

// DownloadVisionZip descarga url a cacheDir verificando el .CHECKSUM.
// Si el zip ya está en caché y verifica, no se vuelve a descargar.
// Devuelve ErrNotPublished si el fichero (o su checksum) da 404.
func DownloadVisionZip(ctx context.Context, url, cacheDir string) (string, error) {
	zipPath := filepath.Join(cacheDir, filepath.Base(url))
	sum, err := fetchChecksumV(ctx, url+".CHECKSUM")
	if err != nil {
		return "", err
	}
	if ok, _ := verifySHA256(zipPath, sum); ok {
		return zipPath, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	if err := downloadFile(ctx, url, zipPath); err != nil {
		return "", err
	}
	if ok, got := verifySHA256(zipPath, sum); !ok {
		return "", fmt.Errorf("bulk: CHECKSUM mismatch for %s: want %s got %s", zipPath, sum, got)
	}
	return zipPath, nil
}

func fetchChecksumV(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%s: %w", url, ErrNotPublished)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bulk: HTTP %d for %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("bulk: malformed CHECKSUM at %s", url)
	}
	return fields[0], nil
}

// Kline1m es una kline 1m oficial parseada (tiempos en ms, precios *1e8).
type Kline1m struct {
	OpenMs     int64
	Open       int64
	High       int64
	Low        int64
	Close      int64
	Volume     int64
	Count      int64
	TakerBuy   int64
}

// StreamKlines1mZip recorre el CSV de klines dentro del ZIP.
// Columnas: open_time,open,high,low,close,volume,close_time,quote_volume,
// count,taker_buy_volume,taker_buy_quote_volume,ignore. Futures usa ms.
func StreamKlines1mZip(path string, fn func(Kline1m) error) (int64, error) {
	return streamZipCSV(path, 12, func(rec []string) error {
		k, err := parseKlineRecord(rec)
		if err != nil {
			return err
		}
		return fn(k)
	})
}

func parseKlineRecord(rec []string) (Kline1m, error) {
	var k Kline1m
	var err error
	if k.OpenMs, err = strconv.ParseInt(rec[0], 10, 64); err != nil {
		return k, err
	}
	if k.Open, err = fixed.Parse(rec[1]); err != nil {
		return k, err
	}
	if k.High, err = fixed.Parse(rec[2]); err != nil {
		return k, err
	}
	if k.Low, err = fixed.Parse(rec[3]); err != nil {
		return k, err
	}
	if k.Close, err = fixed.Parse(rec[4]); err != nil {
		return k, err
	}
	if k.Volume, err = fixed.Parse(rec[5]); err != nil {
		return k, err
	}
	if k.Count, err = strconv.ParseInt(rec[8], 10, 64); err != nil {
		return k, err
	}
	if k.TakerBuy, err = fixed.Parse(rec[9]); err != nil {
		return k, err
	}
	return k, nil
}

// RawTrade es un trade individual del fichero daily/trades (ground truth T+1).
type RawTrade struct {
	ID           int64
	Price        int64
	Qty          int64
	T            int64 // ms
	IsBuyerMaker bool
}

// StreamTradesZip recorre el CSV de trades individuales dentro del ZIP.
// Columnas: id,price,qty,quote_qty,time,is_buyer_maker.
func StreamTradesZip(path string, fn func(RawTrade) error) (int64, error) {
	return streamZipCSV(path, 6, func(rec []string) error {
		var t RawTrade
		var err error
		if t.ID, err = strconv.ParseInt(rec[0], 10, 64); err != nil {
			return err
		}
		if t.Price, err = fixed.Parse(rec[1]); err != nil {
			return err
		}
		if t.Qty, err = fixed.Parse(rec[2]); err != nil {
			return err
		}
		if t.T, err = strconv.ParseInt(rec[4], 10, 64); err != nil {
			return err
		}
		t.IsBuyerMaker = strings.EqualFold(rec[5], "true")
		return fn(t)
	})
}

func streamZipCSV(path string, cols int, fn func([]string) error) (int64, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	var rows int64
	for _, zf := range zr.File {
		if !strings.HasSuffix(zf.Name, ".csv") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return rows, err
		}
		r := csv.NewReader(rc)
		r.ReuseRecord = true
		for {
			rec, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				rc.Close()
				return rows, err
			}
			if len(rec) != cols {
				rc.Close()
				return rows, fmt.Errorf("%s: expected %d columns, got %d", path, cols, len(rec))
			}
			if rec[0][0] < '0' || rec[0][0] > '9' { // cabecera
				continue
			}
			if err := fn(rec); err != nil {
				rc.Close()
				return rows, err
			}
			rows++
		}
		rc.Close()
	}
	if rows == 0 {
		return 0, fmt.Errorf("bulk: no CSV rows in %s", path)
	}
	return rows, nil
}
