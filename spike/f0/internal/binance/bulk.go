// Package binance reads data.binance.vision CSVs and the futures REST API.
package binance

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"f0/internal/candle"
	"f0/internal/fixed"
)

// ReadAggTradesCSV reads a bulk (or REST-dumped) aggTrades CSV:
// agg_trade_id,price,quantity,first_trade_id,last_trade_id,transact_time,is_buyer_maker
// A header row is detected and skipped.
func ReadAggTradesCSV(path string) ([]candle.AggTrade, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true
	var out []candle.AggTrade
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) != 7 {
			return nil, fmt.Errorf("%s: expected 7 columns, got %d", path, len(rec))
		}
		if !isDigit(rec[0][0]) { // header
			continue
		}
		t, err := parseAggRecord(rec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func parseAggRecord(rec []string) (candle.AggTrade, error) {
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
	switch strings.ToLower(rec[6]) {
	case "true":
		t.IsBuyerMaker = true
	case "false":
		t.IsBuyerMaker = false
	default:
		return t, fmt.Errorf("bad is_buyer_maker %q", rec[6])
	}
	return t, nil
}

// Kline is one official kline with timestamps normalized to MILLISECONDS.
// Prices/volumes are fixed-point *1e8.
type Kline struct {
	OpenMs         int64
	Open           int64
	High           int64
	Low            int64
	Close          int64
	Volume         int64
	CloseMs        int64
	Count          int64
	TakerBuyVolume int64
}

// microThreshold: epoch ms values are ~1.8e12, epoch µs ~1.8e15. Anything
// above 1e14 must be microseconds (spot files since 2025-01-01).
const microThreshold = 100_000_000_000_000

// ReadKlinesCSV reads a bulk klines CSV (12 columns, optional header) and
// normalizes open/close times from µs to ms when needed, reporting the
// detected unit via the second return value ("ms" or "us").
func ReadKlinesCSV(path string) ([]Kline, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true
	var out []Kline
	unit := "ms"
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if len(rec) != 12 {
			return nil, "", fmt.Errorf("%s: expected 12 columns, got %d", path, len(rec))
		}
		if !isDigit(rec[0][0]) { // header
			continue
		}
		var k Kline
		if k.OpenMs, err = strconv.ParseInt(rec[0], 10, 64); err != nil {
			return nil, "", err
		}
		if k.CloseMs, err = strconv.ParseInt(rec[6], 10, 64); err != nil {
			return nil, "", err
		}
		if k.OpenMs > microThreshold {
			unit = "us"
			k.OpenMs /= 1000
			k.CloseMs /= 1000
		}
		if k.Open, err = fixed.Parse(rec[1]); err != nil {
			return nil, "", err
		}
		if k.High, err = fixed.Parse(rec[2]); err != nil {
			return nil, "", err
		}
		if k.Low, err = fixed.Parse(rec[3]); err != nil {
			return nil, "", err
		}
		if k.Close, err = fixed.Parse(rec[4]); err != nil {
			return nil, "", err
		}
		if k.Volume, err = fixed.Parse(rec[5]); err != nil {
			return nil, "", err
		}
		if k.Count, err = strconv.ParseInt(rec[8], 10, 64); err != nil {
			return nil, "", err
		}
		if k.TakerBuyVolume, err = fixed.Parse(rec[9]); err != nil {
			return nil, "", err
		}
		out = append(out, k)
	}
	return out, unit, nil
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
