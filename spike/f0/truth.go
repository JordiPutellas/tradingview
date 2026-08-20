package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"

	"f0/internal/binance"
	"f0/internal/candle"
	"f0/internal/fixed"
)

const trades19CSV = dataDir + "/raw/BTCUSDT-trades-" + date + ".csv"

type rawTrade struct {
	ID           int64
	Price        int64
	Qty          int64
	T            int64
	IsBuyerMaker bool
}

func readTradesCSV(path string) ([]rawTrade, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true
	var out []rawTrade
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if rec[0][0] < '0' || rec[0][0] > '9' { // header
			continue
		}
		var t rawTrade
		if t.ID, err = strconv.ParseInt(rec[0], 10, 64); err != nil {
			return nil, err
		}
		if t.Price, err = fixed.Parse(rec[1]); err != nil {
			return nil, err
		}
		if t.Qty, err = fixed.Parse(rec[2]); err != nil {
			return nil, err
		}
		if t.T, err = strconv.ParseInt(rec[4], 10, 64); err != nil {
			return nil, err
		}
		t.IsBuyerMaker = rec[5] == "true"
		out = append(out, t)
	}
	return out, nil
}

// cmdTruth uses the individual-trades bulk file as ground truth to explain
// every V1/V3 discrepancy of the aggTrades reconstruction.
func cmdTruth() error {
	trades, err := readTradesCSV(trades19CSV)
	if err != nil {
		return err
	}
	n := len(trades)
	span := trades[n-1].ID - trades[0].ID + 1
	fmt.Printf("== Trades individuales (bulk) ==\nfilas: %d, ids %d..%d, span %d, ids quemados (no existen): %d\n",
		n, trades[0].ID, trades[n-1].ID, span, span-int64(n))

	// 1m klines rebuilt from individual trades vs official klines.
	klines, _, err := binance.ReadKlinesCSV(fut1mCSV)
	if err != nil {
		return err
	}
	type bucket struct {
		open, high, low, close, vol, buy int64
		count                            int64
	}
	mk := map[int64]*bucket{}
	build := func(dst map[int64]*bucket, tsMs, price, qty int64, maker bool, div int64) {
		key := tsMs / div
		b := dst[key]
		if b == nil {
			b = &bucket{open: price, high: price, low: price}
			dst[key] = b
		}
		if price > b.high {
			b.high = price
		}
		if price < b.low {
			b.low = price
		}
		b.close = price
		b.vol += qty
		if !maker {
			b.buy += qty
		}
		b.count++
	}
	for _, t := range trades {
		build(mk, t.T, t.Price, t.Qty, t.IsBuyerMaker, 60000)
	}
	bad := 0
	for _, k := range klines {
		b := mk[k.OpenMs/60000]
		if b == nil || b.open != k.Open || b.high != k.High || b.low != k.Low || b.close != k.Close ||
			b.vol != k.Volume || b.count != k.Count || b.buy != k.TakerBuyVolume {
			bad++
			if bad <= 5 {
				fmt.Printf("  kline %d no cuadra desde trades individuales: %+v vs oficial\n", k.OpenMs, b)
			}
		}
	}
	fmt.Printf("\n== Klines 1m reconstruidas desde trades INDIVIDUALES vs oficiales ==\nminutos que NO cuadran: %d/%d\n", bad, len(klines))

	// 1s candles: truth (individual trades) vs ours (aggTrades).
	ms := map[int64]*bucket{}
	for _, t := range trades {
		build(ms, t.T, t.Price, t.Qty, t.IsBuyerMaker, 1000)
	}
	aggs, err := binance.ReadAggTradesCSV(bulkAggCSV)
	if err != nil {
		return err
	}
	var b1 candle.Builder
	for _, t := range aggs {
		if err := b1.Add(t); err != nil {
			return err
		}
	}
	ours := b1.Finish()
	var volDiffSecs, ohlcDiffSecs, countDiffSecs, priceDiffFields int
	var maxAbsVolDiff, sumAbsVolDiff, maxAbsPriceDiff, sumAbsPriceDiff int64
	for _, c := range ours {
		tb := ms[c.TsSec]
		if tb == nil {
			volDiffSecs++
			continue
		}
		d := c.Volume - tb.vol
		if d != 0 {
			volDiffSecs++
			if d < 0 {
				d = -d
			}
			sumAbsVolDiff += d
			if d > maxAbsVolDiff {
				maxAbsVolDiff = d
			}
		}
		if c.Open != tb.open || c.High != tb.high || c.Low != tb.low || c.Close != tb.close {
			ohlcDiffSecs++
			for _, pd := range [][2]int64{{c.Open, tb.open}, {c.High, tb.high}, {c.Low, tb.low}, {c.Close, tb.close}} {
				d := pd[0] - pd[1]
				if d < 0 {
					d = -d
				}
				if d > maxAbsPriceDiff {
					maxAbsPriceDiff = d
				}
				sumAbsPriceDiff += d
				if d != 0 {
					priceDiffFields++
				}
			}
		}
		if c.TradeCount != tb.count {
			countDiffSecs++
		}
	}
	secsOnlyInTruth := 0
	oursBySec := map[int64]bool{}
	for _, c := range ours {
		oursBySec[c.TsSec] = true
	}
	for sec := range ms {
		if !oursBySec[sec] {
			secsOnlyInTruth++
		}
	}
	fmt.Printf("\n== Velas 1s: aggTrades vs trades individuales (verdad) ==\n")
	fmt.Printf("segundos con vela (aggTrades): %d, (trades): %d, solo en verdad: %d\n", len(ours), len(ms), secsOnlyInTruth)
	fmt.Printf("segundos con volumen distinto: %d (%.3f%%), |dV| max=%s, |dV| medio en afectados=%s\n",
		volDiffSecs, 100*float64(volDiffSecs)/float64(len(ours)), fixed.Format(maxAbsVolDiff),
		fixed.Format(sumAbsVolDiff/int64(max(volDiffSecs, 1))))
	fmt.Printf("segundos con OHLC distinto: %d (%.3f%%), |dPrecio| max=%s, medio en campos afectados=%s\n",
		ohlcDiffSecs, 100*float64(ohlcDiffSecs)/float64(len(ours)),
		fixed.Format(maxAbsPriceDiff), fixed.Format(sumAbsPriceDiff/int64(max(priceDiffFields, 1))))
	fmt.Printf("segundos con trade_count distinto: %d\n", countDiffSecs)

	// aggTrade timing anatomy: does T match first/last member trade? Do
	// aggregates straddle 1s boundaries?
	byID := make(map[int64]int, n)
	for i, t := range trades {
		byID[t.ID] = i
	}
	multi, tEqFirst, tEqLast, spanGT0, cross1s, cross1m, burnedInside := 0, 0, 0, 0, 0, 0, 0
	var crossExamples []string
	for _, a := range aggs {
		if a.LastTradeID == a.FirstTradeID {
			continue
		}
		multi++
		var minT, maxT int64 = 1 << 62, 0
		for id := a.FirstTradeID; id <= a.LastTradeID; id++ {
			i, ok := byID[id]
			if !ok {
				burnedInside++
				continue
			}
			if trades[i].T < minT {
				minT = trades[i].T
			}
			if trades[i].T > maxT {
				maxT = trades[i].T
			}
		}
		if maxT == 0 {
			continue
		}
		if a.T == minT {
			tEqFirst++
		}
		if a.T == maxT {
			tEqLast++
		}
		if maxT > minT {
			spanGT0++
		}
		if maxT/1000 != minT/1000 {
			cross1s++
			if len(crossExamples) < 5 {
				crossExamples = append(crossExamples, fmt.Sprintf("  aggId=%d T=%d trades %d..%d con tiempos %d..%d (span %d ms)",
					a.ID, a.T, a.FirstTradeID, a.LastTradeID, minT, maxT, maxT-minT))
			}
		}
		if maxT/60000 != minT/60000 {
			cross1m++
		}
	}
	fmt.Printf("\n== Anatomía de aggregates (multi-trade: %d de %d) ==\n", multi, len(aggs))
	fmt.Printf("aggT == tiempo del PRIMER trade: %d, == ÚLTIMO: %d, span>0ms: %d\n", tEqFirst, tEqLast, spanGT0)
	fmt.Printf("aggregates que cruzan borde de 1s: %d, de 1m: %d, ids quemados dentro de aggregates: %d\n", cross1s, cross1m, burnedInside)
	for _, e := range crossExamples {
		fmt.Println(e)
	}
	return nil
}
