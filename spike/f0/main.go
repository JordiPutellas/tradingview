// F0 spike: rebuild 1s candles for BTCUSDT perp (futures UM) from aggTrades
// and validate them against official sources. See RESULTADOS.md.
//
// Subcommands:
//
//	rest -strategy=fromid|starttime   download one day of aggTrades via REST
//	validate                          V1-V4 + V6 against bulk data
//	simulate                          simulate startTime pagination loss on bulk data
//	compare-rest                      V5: bulk vs REST + real pagination loss
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"f0/internal/binance"
	"f0/internal/candle"
	"f0/internal/fixed"
)

const (
	date         = "2026-08-19"
	dataDir      = "data"
	bulkAggCSV   = dataDir + "/raw/BTCUSDT-aggTrades-" + date + ".csv"
	fut1mCSV     = dataDir + "/raw/BTCUSDT-1m-" + date + ".csv"
	spot1sCSV    = dataDir + "/raw/BTCUSDT-1s-" + date + ".csv"
	candlesCSV   = dataDir + "/candles_1s_perp.csv"
	restFromID   = dataDir + "/rest_fromid.csv"
	restStartT   = dataDir + "/rest_starttime.csv"
)

func dayRangeMs() (int64, int64) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	start := t.UTC().UnixMilli()
	return start, start + 86_400_000 - 1
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: f0 rest|validate|simulate|compare-rest")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "rest":
		err = cmdRest(os.Args[2:])
	case "validate":
		err = cmdValidate()
	case "simulate":
		err = cmdSimulate()
	case "compare-rest":
		err = cmdCompareRest()
	case "diag":
		err = cmdDiag()
	case "truth":
		err = cmdTruth()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func cmdRest(args []string) error {
	fs := flag.NewFlagSet("rest", flag.ExitOnError)
	strategy := fs.String("strategy", "fromid", "fromid or starttime")
	fs.Parse(args)
	start, end := dayRangeMs()
	c := binance.NewClient()
	t0 := time.Now()
	var n int
	var err error
	switch *strategy {
	case "fromid":
		n, err = c.DownloadDayFromID(start, end, restFromID)
	case "starttime":
		n, err = c.DownloadDayStartTime(start, end, restStartT)
	default:
		return fmt.Errorf("bad strategy %q", *strategy)
	}
	if err != nil {
		return err
	}
	fmt.Printf("strategy=%s rows=%d requests=%d elapsed=%s\n", *strategy, n, c.Requests, time.Since(t0).Round(time.Second))
	return nil
}

func cmdValidate() error {
	dayStartMs, dayEndMs := dayRangeMs()
	trades, err := binance.ReadAggTradesCSV(bulkAggCSV)
	if err != nil {
		return err
	}
	fmt.Printf("== Datos bulk ==\naggTrades: %d filas, ids %d..%d, T %d..%d\n",
		len(trades), trades[0].ID, trades[len(trades)-1].ID, trades[0].T, trades[len(trades)-1].T)
	if trades[0].T < dayStartMs || trades[len(trades)-1].T > dayEndMs {
		fmt.Println("AVISO: hay trades fuera del rango del día")
	}

	// Ordering sanity + V4: aggTradeId continuity.
	tInversions := 0
	var gaps [][2]int64
	for i := 1; i < len(trades); i++ {
		if trades[i].T < trades[i-1].T {
			tInversions++
		}
		if trades[i].ID != trades[i-1].ID+1 {
			gaps = append(gaps, [2]int64{trades[i-1].ID, trades[i].ID})
		}
	}
	fmt.Printf("\n== V4: continuidad de aggTradeId ==\ninversiones de tiempo: %d\nhuecos de id: %d\n", tInversions, len(gaps))
	for _, g := range gaps {
		fmt.Printf("  hueco: %d -> %d (faltan %d)\n", g[0], g[1], g[1]-g[0]-1)
	}
	expected := trades[len(trades)-1].ID - trades[0].ID + 1
	fmt.Printf("ids esperados por rango: %d, filas: %d\n", expected, int64(len(trades)))

	// Build 1s candles.
	var b candle.Builder
	for _, t := range trades {
		if err := b.Add(t); err != nil {
			return err
		}
	}
	candles := b.Finish()
	if err := writeCandlesCSV(candles); err != nil {
		return err
	}
	emptySecs := 86400 - len(candles)
	fmt.Printf("\n== Velas 1s reconstruidas ==\nvelas: %d, segundos vacíos: %d (%.2f%%)\n",
		len(candles), emptySecs, 100*float64(emptySecs)/86400)

	// Official 1m futures klines.
	klines, unit, err := binance.ReadKlinesCSV(fut1mCSV)
	if err != nil {
		return err
	}
	fmt.Printf("\nklines 1m futures: %d filas (unidad de tiempo detectada: %s)\n", len(klines), unit)

	// V1 (+ per-minute V3): 60x 1s -> 1m vs official.
	mins := candle.ToMinutes(candles)
	minByTs := map[int64]*candle.Minute{}
	for i := range mins {
		minByTs[mins[i].TsMin] = &mins[i]
	}
	type fieldStat struct {
		name string
		bad  int
	}
	stats := []fieldStat{{name: "open"}, {name: "high"}, {name: "low"}, {name: "close"}, {name: "volume"}, {name: "trade_count"}, {name: "buy_volume"}}
	fullMatch, mismatchRows, missingMinutes := 0, 0, 0
	var examples []string
	for _, k := range klines {
		m, ok := minByTs[k.OpenMs/1000]
		if !ok {
			missingMinutes++
			continue
		}
		diffs := []bool{m.Open != k.Open, m.High != k.High, m.Low != k.Low, m.Close != k.Close,
			m.Volume != k.Volume, m.TradeCount != k.Count, m.BuyVolume != k.TakerBuyVolume}
		row := false
		for i, d := range diffs {
			if d {
				stats[i].bad++
				row = true
			}
		}
		if row {
			mismatchRows++
			if len(examples) < 10 {
				examples = append(examples, fmt.Sprintf(
					"  %s: reconstruida O=%s H=%s L=%s C=%s V=%s n=%d bv=%s | oficial O=%s H=%s L=%s C=%s V=%s n=%d bv=%s",
					time.UnixMilli(k.OpenMs).UTC().Format("15:04"),
					fixed.Format(m.Open), fixed.Format(m.High), fixed.Format(m.Low), fixed.Format(m.Close),
					fixed.Format(m.Volume), m.TradeCount, fixed.Format(m.BuyVolume),
					fixed.Format(k.Open), fixed.Format(k.High), fixed.Format(k.Low), fixed.Format(k.Close),
					fixed.Format(k.Volume), k.Count, fixed.Format(k.TakerBuyVolume)))
			}
		} else {
			fullMatch++
		}
	}
	fmt.Printf("\n== V1: 60 velas 1s vs kline 1m oficial (%d minutos) ==\n", len(klines))
	fmt.Printf("minutos que cuadran EXACTOS en todos los campos: %d/%d\n", fullMatch, len(klines))
	fmt.Printf("minutos con alguna discrepancia: %d, minutos sin reconstrucción: %d\n", mismatchRows, missingMinutes)
	for _, s := range stats {
		fmt.Printf("  campo %-11s discrepancias: %d\n", s.name, s.bad)
	}
	for _, e := range examples {
		fmt.Println(e)
	}

	// V2 + V3: day totals.
	var sumQ, sumKV, sumTrades, sumKCount, sumBuy, sumKBuy int64
	for _, t := range trades {
		sumQ += t.Qty
		sumTrades += t.LastTradeID - t.FirstTradeID + 1
		if !t.IsBuyerMaker {
			sumBuy += t.Qty
		}
	}
	for _, k := range klines {
		sumKV += k.Volume
		sumKCount += k.Count
		sumKBuy += k.TakerBuyVolume
	}
	fmt.Printf("\n== V2: volumen del día ==\nsuma q aggTrades:  %s BTC\nsuma klines 1m:    %s BTC\ndiferencia:        %s\n",
		fixed.Format(sumQ), fixed.Format(sumKV), fixed.Format(sumQ-sumKV))
	fmt.Printf("taker-buy aggTrades: %s | taker-buy klines: %s | dif: %s\n",
		fixed.Format(sumBuy), fixed.Format(sumKBuy), fixed.Format(sumBuy-sumKBuy))
	fmt.Printf("\n== V3: conteo de trades individuales ==\nsum(l-f+1) aggTrades: %d\nsum(count) klines 1m: %d\ndiferencia: %d\nfilas de aggTrades (comparación): %d\n",
		sumTrades, sumKCount, sumTrades-sumKCount, len(trades))

	// V6: spot 1s native klines vs reconstructed perp 1s.
	spot, spotUnit, err := binance.ReadKlinesCSV(spot1sCSV)
	if err != nil {
		return err
	}
	fmt.Printf("\n== V6: spot 1s nativo vs perp 1s reconstruido ==\nspot: %d filas (unidad de tiempo detectada: %s)\n", len(spot), spotUnit)
	var spotCounts []int64
	spotEmpty := 0
	for _, k := range spot {
		spotCounts = append(spotCounts, k.Count)
		if k.Count == 0 {
			spotEmpty++
		}
	}
	perpCounts := make([]int64, 0, 86400)
	for _, c := range candles {
		perpCounts = append(perpCounts, c.TradeCount)
	}
	for i := 0; i < emptySecs; i++ {
		perpCounts = append(perpCounts, 0)
	}
	printDist := func(label string, counts []int64, empty int) {
		sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })
		var sum int64
		for _, c := range counts {
			sum += c
		}
		p := func(q float64) int64 { return counts[int(q*float64(len(counts)-1))] }
		fmt.Printf("%s: segundos sin trades: %d (%.2f%%) | trades/s media=%.1f p50=%d p90=%d p99=%d max=%d\n",
			label, empty, 100*float64(empty)/float64(len(counts)), float64(sum)/86400, p(.5), p(.9), p(.99), counts[len(counts)-1])
	}
	printDist("perp (reconstruido)", perpCounts, emptySecs)
	printDist("spot (nativo)      ", spotCounts, spotEmpty)

	// Disk sizes for the retention discussion.
	fmt.Println("\n== Tamaños en disco ==")
	for _, p := range []string{bulkAggCSV, dataDir + "/raw/BTCUSDT-aggTrades-" + date + ".zip", candlesCSV} {
		if st, err := os.Stat(p); err == nil {
			fmt.Printf("%-55s %10.1f MB (x365: %.1f GB/año)\n", p, float64(st.Size())/1e6, float64(st.Size())*365/1e9)
		}
	}
	return nil
}

func writeCandlesCSV(cs []candle.Candle) error {
	f, err := os.Create(candlesCSV)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "ts_utc,open,high,low,close,volume,buy_volume,trade_count,agg_count,first_agg_id,last_agg_id")
	for _, c := range cs {
		fmt.Fprintf(f, "%s,%s,%s,%s,%s,%s,%s,%d,%d,%d,%d\n",
			time.Unix(c.TsSec, 0).UTC().Format("2006-01-02T15:04:05Z"),
			fixed.Format(c.Open), fixed.Format(c.High), fixed.Format(c.Low), fixed.Format(c.Close),
			fixed.Format(c.Volume), fixed.Format(c.BuyVolume), c.TradeCount, c.AggCount, c.FirstAggID, c.LastAggID)
	}
	return nil
}

// cmdSimulate replays startTime=lastT+1 pagination over the bulk file and
// counts exactly which trades a REST client using that strategy would lose.
func cmdSimulate() error {
	trades, err := binance.ReadAggTradesCSV(bulkAggCSV)
	if err != nil {
		return err
	}
	lost := 0
	boundaries := 0
	var examples []int64
	i := 0
	for i < len(trades) {
		end := i + 1000
		if end >= len(trades) {
			break // final partial page: no further pagination, nothing lost
		}
		lastT := trades[end-1].T
		j := end
		for j < len(trades) && trades[j].T <= lastT {
			if len(examples) < 20 {
				examples = append(examples, trades[j].ID)
			}
			j++
		}
		if j > end {
			boundaries++
			lost += j - end
		}
		i = j
	}
	pages := 0
	for k := 0; k < len(trades); k += 1000 {
		pages++
	}
	fmt.Printf("== Simulación de paginación startTime=lastT+1 (páginas de 1000) ==\n")
	fmt.Printf("trades del día: %d\npáginas: ~%d\nbordes de página con pérdida: %d\ntrades PERDIDOS: %d (%.4f%%)\n",
		len(trades), pages, boundaries, lost, 100*float64(lost)/float64(len(trades)))
	fmt.Printf("ejemplos de agg_trade_id perdidos: %v\n", examples)
	return nil
}

func cmdCompareRest() error {
	bulk, err := binance.ReadAggTradesCSV(bulkAggCSV)
	if err != nil {
		return err
	}

	// V5: bulk vs REST fromId — must be identical.
	if rest, err := binance.ReadAggTradesCSV(restFromID); err != nil {
		fmt.Println("rest_fromid.csv no disponible:", err)
	} else {
		fmt.Printf("== V5: bulk (%d filas) vs REST fromId (%d filas) ==\n", len(bulk), len(rest))
		n := min(len(bulk), len(rest))
		diffs := 0
		for i := 0; i < n; i++ {
			a, b := bulk[i], rest[i]
			if a != b {
				diffs++
				if diffs <= 5 {
					fmt.Printf("  dif fila %d: bulk=%+v rest=%+v\n", i, a, b)
				}
			}
		}
		fmt.Printf("filas comparadas: %d, diferencias de contenido: %d, diferencia de longitud: %d\n",
			n, diffs, len(bulk)-len(rest))
	}

	// Real startTime-pagination loss: bulk ids missing from rest_starttime.csv.
	if rest, err := binance.ReadAggTradesCSV(restStartT); err != nil {
		fmt.Println("rest_starttime.csv no disponible:", err)
	} else {
		fmt.Printf("\n== Pérdida REAL por paginación startTime (%d filas vs %d bulk) ==\n", len(rest), len(bulk))
		have := make(map[int64]bool, len(rest))
		dupes := 0
		for _, t := range rest {
			if have[t.ID] {
				dupes++
			}
			have[t.ID] = true
		}
		var missing []int64
		for _, t := range bulk {
			if !have[t.ID] {
				missing = append(missing, t.ID)
			}
		}
		extras := len(rest) - dupes - (len(bulk) - len(missing))
		fmt.Printf("trades PERDIDOS: %d (%.4f%%), duplicados en rest: %d, extras: %d\n",
			len(missing), 100*float64(len(missing))/float64(len(bulk)), dupes, extras)
		show := missing
		if len(show) > 20 {
			show = show[:20]
		}
		fmt.Printf("ejemplos de ids perdidos: %v\n", show)
		_ = strconv.IntSize
	}
	return nil
}
