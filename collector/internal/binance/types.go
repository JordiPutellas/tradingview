// Package binance habla con Binance Futures UM: stream WS de aggTrades,
// REST para reconciliación y data.binance.vision para backfill.
package binance

import (
	"fmt"

	"jputellas.dev/btcdash/collector/internal/candle"
	"jputellas.dev/btcdash/collector/internal/fixed"
)

// aggTradePayload es el formato común del evento WS y de la respuesta REST
// (el WS lo envuelve con e/E/s; los campos de trade son los mismos).
type aggTradePayload struct {
	A int64  `json:"a"`
	P string `json:"p"`
	Q string `json:"q"`
	F int64  `json:"f"`
	L int64  `json:"l"`
	T int64  `json:"T"`
	M bool   `json:"m"`
}

func (p aggTradePayload) toCandle() (candle.AggTrade, error) {
	price, err := fixed.Parse(p.P)
	if err != nil {
		return candle.AggTrade{}, fmt.Errorf("aggTrade %d: bad price %q: %w", p.A, p.P, err)
	}
	qty, err := fixed.Parse(p.Q)
	if err != nil {
		return candle.AggTrade{}, fmt.Errorf("aggTrade %d: bad qty %q: %w", p.A, p.Q, err)
	}
	return candle.AggTrade{
		ID: p.A, Price: price, Qty: qty,
		FirstTradeID: p.F, LastTradeID: p.L,
		T: p.T, IsBuyerMaker: p.M,
	}, nil
}
