package main

import (
	"fmt"
	"time"

	"f0/internal/binance"
	"f0/internal/candle"
	"f0/internal/fixed"
)

// cmdDiag characterizes the two V1 discrepancy families:
//  1. minute-boundary moves: official klines assign some trades to a different
//     minute than their aggTrade transact_time implies;
//  2. phantom trade ids: sum(last_trade_id-first_trade_id+1) overcounts the
//     official kline `count`.
func cmdDiag() error {
	trades, err := binance.ReadAggTradesCSV(bulkAggCSV)
	if err != nil {
		return err
	}
	klines, _, err := binance.ReadKlinesCSV(fut1mCSV)
	if err != nil {
		return err
	}

	// Rebuild per-minute aggregates directly from trades.
	var b candle.Builder
	for _, t := range trades {
		if err := b.Add(t); err != nil {
			return err
		}
	}
	mins := candle.ToMinutes(b.Finish())
	minByTs := map[int64]*candle.Minute{}
	for i := range mins {
		minByTs[mins[i].TsMin] = &mins[i]
	}

	// Volume delta per minute (recon - official).
	type mdelta struct {
		tsMin  int64
		dVol   int64
		dCount int64
	}
	var deltas []mdelta
	for _, k := range klines {
		m := minByTs[k.OpenMs/1000]
		if m == nil {
			continue
		}
		deltas = append(deltas, mdelta{k.OpenMs / 1000, m.Volume - k.Volume, m.TradeCount - k.Count})
	}

	// 1) Boundary moves: adjacent minute pairs with equal-and-opposite volume deltas.
	pairs, unpaired := 0, 0
	for i := 0; i < len(deltas); i++ {
		if deltas[i].dVol == 0 {
			continue
		}
		if i+1 < len(deltas) && deltas[i].dVol > 0 && deltas[i+1].dVol == -deltas[i].dVol {
			pairs++
		} else if i > 0 && deltas[i].dVol < 0 && deltas[i-1].dVol == -deltas[i].dVol {
			// counted as the pair's tail
		} else {
			unpaired++
			fmt.Printf("minuto %s con delta de volumen NO emparejado: %s\n",
				time.Unix(deltas[i].tsMin, 0).UTC().Format("15:04"), fixed.Format(deltas[i].dVol))
		}
	}
	fmt.Printf("== Movimientos de borde de minuto ==\n")
	fmt.Printf("pares de minutos adyacentes con delta igual y opuesto: %d, deltas sin emparejar: %d\n", pairs, unpaired)

	// Identify the moved trades: for each positive-delta minute, walk its tail.
	byMinute := map[int64][]candle.AggTrade{}
	for _, t := range trades {
		byMinute[t.T/60000*60] = append(byMinute[t.T/60000*60], t)
	}
	shown := 0
	offsetHist := map[int64]int{} // T offset within minute (ms from :59.000) of moved trades
	movedTotal := 0
	splitAggs := 0 // moved trades that are part of an agg row also containing non-moved trades
	for i := 0; i < len(deltas)-1; i++ {
		d := deltas[i].dVol
		if d <= 0 || deltas[i+1].dVol != -d {
			continue
		}
		tl := byMinute[deltas[i].tsMin]
		var acc int64
		var moved []candle.AggTrade
		for j := len(tl) - 1; j >= 0 && acc < d && len(moved) < 50; j-- {
			acc += tl[j].Qty
			moved = append(moved, tl[j])
		}
		if acc != d {
			fmt.Printf("  minuto %s: la cola no suma el delta exacto (delta=%s, acumulado=%s) — patrón distinto\n",
				time.Unix(deltas[i].tsMin, 0).UTC().Format("15:04"), fixed.Format(d), fixed.Format(acc))
			continue
		}
		movedTotal += len(moved)
		for _, t := range moved {
			offsetHist[t.T%60000-59000]++
			if t.LastTradeID-t.FirstTradeID+1 > 1 {
				splitAggs++
			}
		}
		if shown < 5 {
			shown++
			for _, t := range moved {
				fmt.Printf("  movido a minuto siguiente: aggId=%d T=%s.%03d qty=%s trades[%d..%d]\n",
					t.ID, time.UnixMilli(t.T).UTC().Format("15:04:05"), t.T%1000,
					fixed.Format(t.Qty), t.FirstTradeID, t.LastTradeID)
			}
		}
	}
	fmt.Printf("trades movidos identificados: %d (aggRows multi-trade entre ellos: %d)\n", movedTotal, splitAggs)
	fmt.Printf("histograma de offset dentro del minuto (ms desde :59.000): %v\n", offsetHist)

	// 2) Phantom ids: underlying trade-id coverage vs sums.
	var sumLF int64
	var interGap int64
	interGapRows := 0
	for i, t := range trades {
		sumLF += t.LastTradeID - t.FirstTradeID + 1
		if i > 0 {
			g := t.FirstTradeID - trades[i-1].LastTradeID - 1
			if g != 0 {
				interGap += g
				interGapRows++
			}
		}
	}
	var sumKCount int64
	for _, k := range klines {
		sumKCount += k.Count
	}
	span := trades[len(trades)-1].LastTradeID - trades[0].FirstTradeID + 1
	fmt.Printf("\n== IDs de trade individuales ==\n")
	fmt.Printf("rango cubierto (last.l - first.f + 1): %d\n", span)
	fmt.Printf("sum(l-f+1): %d\n", sumLF)
	fmt.Printf("ids ausentes ENTRE filas de aggTrades: %d (en %d posiciones)\n", interGap, interGapRows)
	fmt.Printf("sum(count) klines: %d\n", sumKCount)
	fmt.Printf("sum(l-f+1) - sum(count) = %d\n", sumLF-sumKCount)

	// Count-delta distribution for minutes with NO volume delta (pure phantom effect).
	pure := map[int64]int{}
	var pureSum int64
	for _, d := range deltas {
		if d.dVol == 0 && d.dCount != 0 {
			pure[d.dCount]++
			pureSum += d.dCount
		}
	}
	fmt.Printf("\nminutos con volumen exacto pero count distinto: distribución de dCount=%v, suma=%d\n", pure, pureSum)
	return nil
}
