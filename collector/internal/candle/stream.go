package candle

import "fmt"

// Stream es la variante streaming del Builder para el colector 24/7: emite
// cada vela cerrada por callback en cuanto llega un trade del segundo
// siguiente, sin acumular en memoria. Misma semántica de agregación que
// Builder (validada en F0).
type Stream struct {
	Emit func(Candle)
	cur  *Candle
}

// Add ingiere un trade. Los trades deben llegar con timestamp no decreciente
// (garantizado por el orden de aggTradeId; verificado en F0: 0 inversiones).
func (s *Stream) Add(t AggTrade) error {
	sec := t.T / 1000
	if s.cur != nil && sec < s.cur.TsSec {
		return fmt.Errorf("out-of-order trade id=%d: sec %d < current %d", t.ID, sec, s.cur.TsSec)
	}
	if s.cur == nil || sec > s.cur.TsSec {
		if s.cur != nil {
			s.Emit(*s.cur)
		}
		s.cur = &Candle{
			TsSec: sec, Open: t.Price, High: t.Price, Low: t.Price,
			FirstAggID: t.ID,
		}
	}
	c := s.cur
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

// Flush emite la vela en curso (parcial). Se usa en el apagado ordenado; al
// rearrancar, la reconciliación reconstruye ese segundo completo y lo
// sobreescribe.
func (s *Stream) Flush() {
	if s.cur != nil {
		s.Emit(*s.cur)
		s.cur = nil
	}
}

// Current devuelve una copia de la vela en curso (parcial), o nil.
func (s *Stream) Current() *Candle {
	if s.cur == nil {
		return nil
	}
	c := *s.cur
	return &c
}

// CurrentSec devuelve el segundo de la vela en curso, o -1 si no hay.
func (s *Stream) CurrentSec() int64 {
	if s.cur == nil {
		return -1
	}
	return s.cur.TsSec
}
