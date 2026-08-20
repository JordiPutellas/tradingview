package candle

import (
	"reflect"
	"testing"
)

// e8 convierte unidades enteras a punto fijo 1e8.
const e8 = 100_000_000

func mkTrade(id, priceE8, qtyE8, first, last, tMs int64, maker bool) AggTrade {
	return AggTrade{ID: id, Price: priceE8, Qty: qtyE8, FirstTradeID: first, LastTradeID: last, T: tMs, IsBuyerMaker: maker}
}

// Casos calcados de la semántica validada en F0.
var trades = []AggTrade{
	// segundo 1000: dos aggregates, uno taker-buy (m=false) y otro taker-sell
	mkTrade(1, 64000*e8, 2*e8, 100, 102, 1000_015, false), // 3 trades individuales
	mkTrade(2, 64010*e8, 1*e8, 103, 103, 1000_900, true),
	// segundo 1001: un trade justo en el borde .000 (pertenece a 1001, no a 1000)
	mkTrade(3, 63990*e8, 5*e8, 104, 106, 1001_000, true),
	// segundo 1003 (el 1002 queda vacío: no genera vela)
	mkTrade(4, 64020*e8, 1*e8, 107, 107, 1003_999, false),
}

var want = []Candle{
	{TsSec: 1000, Open: 64000 * e8, High: 64010 * e8, Low: 64000 * e8, Close: 64010 * e8,
		Volume: 3 * e8, BuyVolume: 2 * e8, TradeCount: 4, AggCount: 2, FirstAggID: 1, LastAggID: 2},
	{TsSec: 1001, Open: 63990 * e8, High: 63990 * e8, Low: 63990 * e8, Close: 63990 * e8,
		Volume: 5 * e8, BuyVolume: 0, TradeCount: 3, AggCount: 1, FirstAggID: 3, LastAggID: 3},
	{TsSec: 1003, Open: 64020 * e8, High: 64020 * e8, Low: 64020 * e8, Close: 64020 * e8,
		Volume: 1 * e8, BuyVolume: 1 * e8, TradeCount: 1, AggCount: 1, FirstAggID: 4, LastAggID: 4},
}

func TestBuilderAggregation(t *testing.T) {
	var b Builder
	for _, tr := range trades {
		if err := b.Add(tr); err != nil {
			t.Fatal(err)
		}
	}
	got := b.Finish()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Builder:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestStreamMatchesBuilder(t *testing.T) {
	var got []Candle
	s := Stream{Emit: func(c Candle) { got = append(got, c) }}
	for _, tr := range trades {
		if err := s.Add(tr); err != nil {
			t.Fatal(err)
		}
	}
	s.Flush()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Stream:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestStreamOutOfOrderRejected(t *testing.T) {
	s := Stream{Emit: func(Candle) {}}
	if err := s.Add(mkTrade(1, e8, e8, 1, 1, 5000_000, false)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(mkTrade(2, e8, e8, 2, 2, 4000_000, false)); err == nil {
		t.Error("expected error on out-of-order trade, got nil")
	}
}

func TestStreamCurrentIsCopy(t *testing.T) {
	s := Stream{Emit: func(Candle) {}}
	if err := s.Add(mkTrade(1, e8, e8, 1, 1, 5000_500, false)); err != nil {
		t.Fatal(err)
	}
	cur := s.Current()
	if cur == nil || cur.TsSec != 5000 {
		t.Fatalf("Current() = %+v", cur)
	}
	cur.Volume = 999 // no debe afectar al estado interno
	if s.Current().Volume != e8 {
		t.Error("Current() must return a copy")
	}
	if s.CurrentSec() != 5000 {
		t.Errorf("CurrentSec() = %d", s.CurrentSec())
	}
	s.Flush()
	if s.Current() != nil || s.CurrentSec() != -1 {
		t.Error("after Flush there must be no current candle")
	}
}

func TestToMinutes(t *testing.T) {
	var b Builder
	for _, tr := range trades {
		if err := b.Add(tr); err != nil {
			t.Fatal(err)
		}
	}
	mins := ToMinutes(b.Finish())
	// 1000..1003s caen en minutos 960 (16*60) y ... 1000/60=16 → min 960; 1003/60=16 → mismo minuto.
	if len(mins) != 1 {
		t.Fatalf("expected 1 minute, got %d", len(mins))
	}
	m := mins[0]
	if m.TsMin != 960 || m.Open != 64000*e8 || m.Close != 64020*e8 ||
		m.Volume != 9*e8 || m.TradeCount != 8 {
		t.Errorf("minute = %+v", m)
	}
}
