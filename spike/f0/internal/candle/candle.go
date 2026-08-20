// Package candle builds 1-second candles from Binance aggTrades.
// This is the aggregation logic intended for reuse in F1a.
//
// Design decisions (see also spike/f0/RESULTADOS.md):
//   - A trade belongs to the second floor(T_ms/1000). Official klines close at
//     .999 ms, so truncation matches Binance bucketing with no edge ambiguity.
//   - Seconds with no trades produce NO candle (RF-2.3).
//   - TradeCount counts INDIVIDUAL trades (sum of last_trade_id-first_trade_id+1),
//     matching the `count` field of official klines. AggCount counts aggTrade rows.
//   - BuyVolume is taker-buy volume: quantity where is_buyer_maker == false,
//     matching `taker_buy_volume` of official klines.
package candle

import "fmt"

// AggTrade is one aggregated trade. Price/Qty are fixed-point *1e8. T is in ms.
type AggTrade struct {
	ID           int64
	Price        int64
	Qty          int64
	FirstTradeID int64
	LastTradeID  int64
	T            int64
	IsBuyerMaker bool
}

// Candle is a 1-second candle. TsSec is the UTC epoch second (bucket start).
type Candle struct {
	TsSec      int64
	Open       int64
	High       int64
	Low        int64
	Close      int64
	Volume     int64
	BuyVolume  int64
	TradeCount int64 // individual trades (kline semantics)
	AggCount   int64 // aggTrade rows
	FirstAggID int64
	LastAggID  int64
}

// Builder consumes aggTrades in time order and emits closed 1s candles.
type Builder struct {
	cur     *Candle
	candles []Candle
}

// Add ingests one trade. Trades must arrive with non-decreasing timestamps.
func (b *Builder) Add(t AggTrade) error {
	sec := t.T / 1000
	if b.cur != nil && sec < b.cur.TsSec {
		return fmt.Errorf("out-of-order trade id=%d: sec %d < current %d", t.ID, sec, b.cur.TsSec)
	}
	if b.cur == nil || sec > b.cur.TsSec {
		if b.cur != nil {
			b.candles = append(b.candles, *b.cur)
		}
		b.cur = &Candle{
			TsSec: sec, Open: t.Price, High: t.Price, Low: t.Price,
			FirstAggID: t.ID,
		}
	}
	c := b.cur
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
	c.TradeCount += t.LastTradeID - t.FirstTradeID + 1
	c.AggCount++
	c.LastAggID = t.ID
	return nil
}

// Finish closes the pending candle and returns all candles.
func (b *Builder) Finish() []Candle {
	if b.cur != nil {
		b.candles = append(b.candles, *b.cur)
		b.cur = nil
	}
	return b.candles
}

// Minute is a 1-minute aggregate of 1s candles, for validation against
// official 1m klines.
type Minute struct {
	TsMin      int64 // epoch minute start, seconds
	Open       int64
	High       int64
	Low        int64
	Close      int64
	Volume     int64
	BuyVolume  int64
	TradeCount int64
}

// ToMinutes aggregates 1s candles (time-ordered) into 1-minute buckets.
func ToMinutes(cs []Candle) []Minute {
	var out []Minute
	var cur *Minute
	for i := range cs {
		c := &cs[i]
		min := c.TsSec / 60 * 60
		if cur == nil || min > cur.TsMin {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Minute{TsMin: min, Open: c.Open, High: c.High, Low: c.Low}
		}
		if c.High > cur.High {
			cur.High = c.High
		}
		if c.Low < cur.Low {
			cur.Low = c.Low
		}
		cur.Close = c.Close
		cur.Volume += c.Volume
		cur.BuyVolume += c.BuyVolume
		cur.TradeCount += c.TradeCount
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}
