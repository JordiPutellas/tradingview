package collect

import (
	"context"
	"sync"
	"testing"
	"time"

	"jputellas.dev/btcdash/collector/internal/candle"
	"jputellas.dev/btcdash/collector/internal/health"
	"jputellas.dev/btcdash/collector/internal/store"
)

// ---- fakes ----

type fakeStore struct {
	mu      sync.Mutex
	candles map[int64]store.StoredCandle
	gaps    []store.Gap
	preset  *store.StoredCandle // respuesta de LastCandle al arrancar
}

func newFakeStore() *fakeStore {
	return &fakeStore{candles: map[int64]store.StoredCandle{}}
}

func (f *fakeStore) UpsertCandles(_ context.Context, cs []store.StoredCandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range cs {
		f.candles[c.TsSec] = c
	}
	return nil
}

func (f *fakeStore) LastCandle(context.Context, string) (*store.StoredCandle, error) {
	return f.preset, nil
}

func (f *fakeStore) InsertGap(_ context.Context, g store.Gap) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g.ID = int64(len(f.gaps) + 1)
	f.gaps = append(f.gaps, g)
	return g.ID, nil
}

func (f *fakeStore) UpdateGapStatus(_ context.Context, id int64, status, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.gaps {
		if f.gaps[i].ID == id {
			f.gaps[i].Status = status
			if reason != "" {
				f.gaps[i].Reason = reason
			}
		}
	}
	return nil
}

func (f *fakeStore) OpenGapCount(context.Context, string) (int, error) { return 0, nil }

func (f *fakeStore) snapshot() (map[int64]store.StoredCandle, []store.Gap) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cs := make(map[int64]store.StoredCandle, len(f.candles))
	for k, v := range f.candles {
		cs[k] = v
	}
	return cs, append([]store.Gap(nil), f.gaps...)
}

type fakeRest struct {
	trades   []candle.AggTrade
	pageSize int
}

func (f *fakeRest) AggTradesFrom(_ context.Context, fromID int64) ([]candle.AggTrade, error) {
	var out []candle.AggTrade
	for _, t := range f.trades {
		if t.ID >= fromID {
			out = append(out, t)
			if len(out) == max(f.pageSize, 1000) {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeRest) AggTradesSince(_ context.Context, sinceMs int64) ([]candle.AggTrade, error) {
	var out []candle.AggTrade
	for _, t := range f.trades {
		if t.T >= sinceMs {
			out = append(out, t)
			if len(out) == max(f.pageSize, 1000) {
				break
			}
		}
	}
	return out, nil
}

// scriptedWS entrega los lotes en orden y luego bloquea hasta cancelación.
type scriptedWS struct{ batches [][]candle.AggTrade }

func (w *scriptedWS) Run(ctx context.Context, onTrade func(candle.AggTrade)) {
	for _, b := range w.batches {
		for _, t := range b {
			select {
			case <-ctx.Done():
				return
			default:
			}
			onTrade(t)
		}
	}
	<-ctx.Done()
}

// ---- helpers ----

const e8 = 100_000_000

func tr(id, sec int64, offMs int64) candle.AggTrade {
	return candle.AggTrade{
		ID: id, Price: 64000 * e8, Qty: 1 * e8,
		FirstTradeID: id * 10, LastTradeID: id*10 + 1, // 2 trades individuales por aggregate
		T: sec*1000 + offMs,
	}
}

func runCollector(t *testing.T, fs *fakeStore, rest *fakeRest, ws *scriptedWS, now time.Time, wantSecs []int64) {
	t.Helper()
	col := &Collector{
		Symbol: "BTCUSDT", Store: fs, Rest: rest, WS: ws,
		Health:          health.New(30 * time.Second),
		ReconcileWindow: 40 * time.Hour,
		BufferSize:      1024,
		Now:             func() time.Time { return now },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- col.Run(ctx) }()

	deadline := time.After(10 * time.Second)
	for {
		cs, _ := fs.snapshot()
		missing := false
		for _, s := range wantSecs[:len(wantSecs)-1] { // la última se escribe al hacer Flush en el apagado
			if _, ok := cs[s]; !ok {
				missing = true
			}
		}
		if !missing {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("timeout waiting for candles; have %v", keys(cs))
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("collector returned error: %v", err)
	}
	cs, _ := fs.snapshot()
	for _, s := range wantSecs {
		if _, ok := cs[s]; !ok {
			t.Errorf("missing candle for second %d", s)
		}
	}
}

func keys(m map[int64]store.StoredCandle) []int64 {
	var out []int64
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- tests ----

// Hueco en vivo: el WS salta de id 6 a 15; el REST fake tiene 7..14. Todo el
// rango debe quedar cubierto, el hueco resuelto y las velas con trades REST
// marcadas 'reconciled'.
func TestLiveGapReconciled(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	// En vivo: ids 1,2 (sec 1000) y 3 (sec 1001); luego el WS salta a 12:
	// hueco de ids 4..11, que el REST fake tiene en los secs 1001-1003.
	batch1 := []candle.AggTrade{tr(1, 1000, 15), tr(2, 1000, 400), tr(3, 1001, 100)}
	restTrades := []candle.AggTrade{
		tr(4, 1001, 300), tr(5, 1001, 400), tr(6, 1001, 500),
		tr(7, 1002, 0), tr(8, 1002, 100), tr(9, 1002, 200),
		tr(10, 1003, 0), tr(11, 1003, 100),
	}
	batch2 := []candle.AggTrade{tr(12, 1004, 100), tr(13, 1005, 100)}

	fs := newFakeStore()
	runCollector(t, fs,
		&fakeRest{trades: restTrades},
		&scriptedWS{batches: [][]candle.AggTrade{batch1, batch2}},
		now, []int64{1000, 1001, 1002, 1003, 1004, 1005})

	cs, gaps := fs.snapshot()
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %+v", gaps)
	}
	if gaps[0].Status != store.GapResolved {
		t.Errorf("gap status = %s, want resolved", gaps[0].Status)
	}
	if gaps[0].FirstMissingID != 4 || gaps[0].LastMissingID != 11 {
		t.Errorf("gap ids = [%d,%d], want [4,11]", gaps[0].FirstMissingID, gaps[0].LastMissingID)
	}
	// 1000 es 100% en vivo; 1001-1003 contienen trades REST; 1004+ vuelven a vivo.
	if q := cs[1000].Quality; q != store.QualityRealtime {
		t.Errorf("candle 1000 quality = %s", q)
	}
	for _, s := range []int64{1001, 1002, 1003} {
		if q := cs[s].Quality; q != store.QualityReconciled {
			t.Errorf("candle %d quality = %s, want reconciled", s, q)
		}
	}
	if q := cs[1004].Quality; q != store.QualityRealtime {
		t.Errorf("candle 1004 quality = %s, want realtime", q)
	}
	// Continuidad: la suma de volumen debe cubrir los 13 aggregates (1 BTC cada uno).
	var vol int64
	for _, c := range cs {
		vol += c.Volume
	}
	if vol != 13*e8 {
		t.Errorf("total volume = %d, want %d", vol, 13*e8)
	}
}

// Idempotencia: procesar el mismo rango dos veces (rearranque con solape WS)
// no duplica ni corrompe.
func TestIdempotentReprocessing(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	trades := []candle.AggTrade{
		tr(1, 1000, 15), tr(2, 1000, 400), tr(3, 1001, 100), tr(4, 1002, 100),
	}
	fs := newFakeStore()
	// El WS entrega el mismo lote dos veces (solape completo tras reconexión).
	runCollector(t, fs, &fakeRest{}, &scriptedWS{batches: [][]candle.AggTrade{trades, trades}},
		now, []int64{1000, 1001, 1002})
	cs, gaps := fs.snapshot()
	if len(gaps) != 0 {
		t.Fatalf("no gaps expected, got %+v", gaps)
	}
	if c := cs[1000]; c.Volume != 2*e8 || c.AggCount != 2 || c.TradeCount != 4 {
		t.Errorf("candle 1000 corrupted by reprocessing: %+v", c)
	}
	if c := cs[1001]; c.Volume != 1*e8 || c.AggCount != 1 {
		t.Errorf("candle 1001 corrupted: %+v", c)
	}
}

// Arranque con última vela más vieja que la ventana: la parte vieja va a
// pending_bulk (ALERTA) y la parte reciente se reconcilia por startTime.
func TestStartupGapOlderThanWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	oldSec := now.Add(-100 * time.Hour).Unix()
	cutoffMs := now.Add(-40 * time.Hour).UnixMilli()

	fs := newFakeStore()
	fs.preset = &store.StoredCandle{
		Candle: candle.Candle{TsSec: oldSec, FirstAggID: 100, LastAggID: 110},
		Symbol: "BTCUSDT", Quality: store.QualityRealtime,
	}
	// REST tiene trades desde justo después del cutoff.
	restSec := cutoffMs/1000 + 10
	liveSec := now.Unix()
	live := []candle.AggTrade{tr(5000, liveSec, 0), tr(5001, liveSec+1, 0)}
	// El REST real también ve los trades que están llegando en vivo; la
	// paginación debe pararse sola en untilID sin tocarlos.
	restTrades := []candle.AggTrade{
		tr(4990, restSec, 100), tr(4991, restSec, 300), tr(4992, restSec+1, 0),
		live[0], live[1],
	}

	runCollector(t, fs, &fakeRest{trades: restTrades},
		&scriptedWS{batches: [][]candle.AggTrade{live}},
		now, []int64{restSec, restSec + 1, liveSec, liveSec + 1})

	cs, gaps := fs.snapshot()
	var bulk, resolved int
	for _, g := range gaps {
		switch g.Status {
		case store.GapPendingBulk:
			bulk++
		case store.GapResolved:
			resolved++
		}
	}
	if bulk != 1 || resolved != 1 {
		t.Fatalf("expected 1 pending_bulk + 1 resolved, got %+v", gaps)
	}
	if q := cs[restSec].Quality; q != store.QualityReconciled {
		t.Errorf("REST-recovered candle quality = %s", q)
	}
	if q := cs[liveSec].Quality; q != store.QualityRealtime {
		t.Errorf("live candle quality = %s", q)
	}
}
