//go:build live

package collect

import (
	"context"
	"sort"
	"testing"
	"time"

	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/fixed"
	"jputellas.dev/btcdash/collector/internal/health"
)

// Smoke test contra el stream REAL de Binance (sin BD): verifica que el
// pipeline WS → parseo → agregación 1s produce velas contiguas con datos
// reales. Ejecutar a mano:
//
//	go test -tags live -run TestLiveSmoke -v ./internal/collect/
func TestLiveSmoke(t *testing.T) {
	fs := newFakeStore()
	col := &Collector{
		Symbol: "BTCUSDT",
		Store:  fs,
		Rest:   binance.NewREST("https://fapi.binance.com", "BTCUSDT"),
		WS:     &binance.WSClient{BaseURL: "wss://fstream.binance.com/market/ws", Symbol: "BTCUSDT"},
		Health: health.New(30 * time.Second),

		ReconcileWindow: 40 * time.Hour,
		BufferSize:      4096,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := col.Run(ctx); err != nil {
		t.Fatalf("collector: %v", err)
	}

	cs, gaps := fs.snapshot()
	if len(cs) < 20 {
		t.Fatalf("expected >=20 candles in 45s of BTCUSDT perp, got %d", len(cs))
	}
	secs := make([]int64, 0, len(cs))
	for s := range cs {
		secs = append(secs, s)
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
	var vol int64
	for i, s := range secs {
		c := cs[s]
		vol += c.Volume
		if c.High < c.Low || c.Open == 0 || c.Close == 0 || c.AggCount == 0 {
			t.Errorf("malformed candle at %d: %+v", s, c)
		}
		// Continuidad de aggTradeId entre velas consecutivas (aunque haya
		// segundos vacíos por medio): es la garantía RNF-5.
		if i > 0 {
			prev := cs[secs[i-1]]
			if c.FirstAggID != prev.LastAggID+1 {
				t.Errorf("agg id discontinuity between %d and %d: %d -> %d",
					secs[i-1], s, prev.LastAggID, c.FirstAggID)
			}
		}
	}
	t.Logf("LIVE OK: %d velas [%s..%s], volumen total %s BTC, gaps=%d",
		len(cs), time.Unix(secs[0], 0).UTC().Format("15:04:05"),
		time.Unix(secs[len(secs)-1], 0).UTC().Format("15:04:05"),
		fixed.Format(vol), len(gaps))
}
