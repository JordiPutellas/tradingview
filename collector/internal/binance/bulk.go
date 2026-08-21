package binance

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"jputellas.dev/btcdash/collector/internal/candle"
	"jputellas.dev/btcdash/collector/internal/fixed"
)

// DownloadDailyAggTrades descarga el ZIP diario de aggTrades a cacheDir,
// verifica su .CHECKSUM (sha256) y devuelve la ruta local. Si el fichero ya
// existe y verifica, no se vuelve a descargar (reanudable, RF-1.5).
// Devuelve ErrNotPublished (envuelto) si el día aún no está en el bucket.
func DownloadDailyAggTrades(ctx context.Context, symbol string, day time.Time, cacheDir string) (string, error) {
	url := fmt.Sprintf("%s/daily/aggTrades/%s/%s-aggTrades-%s.zip",
		visionBase, symbol, symbol, day.Format("2006-01-02"))
	return DownloadVisionZip(ctx, url, cacheDir)
}

func downloadFile(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bulk: HTTP %d for %s", resp.StatusCode, url)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func verifySHA256(path, want string) (bool, string) {
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, ""
	}
	got := hex.EncodeToString(h.Sum(nil))
	return got == want, got
}

// StreamAggTradesZip recorre el CSV dentro del ZIP en streaming (sin volcar
// 137 MB a disco ni a memoria) y entrega cada aggTrade en orden.
func StreamAggTradesZip(path string, fn func(candle.AggTrade) error) (int64, error) {
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
			if len(rec) != 7 {
				rc.Close()
				return rows, fmt.Errorf("bulk: expected 7 columns, got %d", len(rec))
			}
			if rec[0][0] < '0' || rec[0][0] > '9' { // cabecera
				continue
			}
			t, err := parseAggCSV(rec)
			if err != nil {
				rc.Close()
				return rows, err
			}
			if err := fn(t); err != nil {
				rc.Close()
				return rows, err
			}
			rows++
		}
		rc.Close()
	}
	if rows == 0 {
		return 0, fmt.Errorf("bulk: no CSV rows found in %s", path)
	}
	return rows, nil
}

// parseAggCSV: agg_trade_id,price,quantity,first_trade_id,last_trade_id,transact_time,is_buyer_maker
func parseAggCSV(rec []string) (candle.AggTrade, error) {
	var t candle.AggTrade
	var err error
	if t.ID, err = strconv.ParseInt(rec[0], 10, 64); err != nil {
		return t, err
	}
	if t.Price, err = fixed.Parse(rec[1]); err != nil {
		return t, err
	}
	if t.Qty, err = fixed.Parse(rec[2]); err != nil {
		return t, err
	}
	if t.FirstTradeID, err = strconv.ParseInt(rec[3], 10, 64); err != nil {
		return t, err
	}
	if t.LastTradeID, err = strconv.ParseInt(rec[4], 10, 64); err != nil {
		return t, err
	}
	if t.T, err = strconv.ParseInt(rec[5], 10, 64); err != nil {
		return t, err
	}
	t.IsBuyerMaker = strings.EqualFold(rec[6], "true")
	return t, nil
}
