package reconcile

import (
	"context"
	"testing"
	"time"

	"jputellas.dev/btcdash/collector/internal/candle"
)

var (
	now    = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	window = 40 * time.Hour
	cutoff = now.Add(-window)
)

func TestClassifyAllRecent(t *testing.T) {
	p := Classify(now.Add(-2*time.Hour), now.Add(-time.Minute), now, window)
	if p.Bulk != nil || p.Rest == nil {
		t.Fatalf("plan = %+v", p)
	}
}

func TestClassifyAllOld(t *testing.T) {
	p := Classify(now.Add(-80*time.Hour), now.Add(-50*time.Hour), now, window)
	if p.Rest != nil || p.Bulk == nil {
		t.Fatalf("plan = %+v", p)
	}
}

func TestClassifyStraddle(t *testing.T) {
	start, end := now.Add(-60*time.Hour), now.Add(-time.Hour)
	p := Classify(start, end, now, window)
	if p.Bulk == nil || p.Rest == nil {
		t.Fatalf("plan = %+v", p)
	}
	if !p.Bulk.Start.Equal(start) || !p.Bulk.End.Equal(cutoff) {
		t.Errorf("bulk = %+v, want [%v, %v]", p.Bulk, start, cutoff)
	}
	if !p.Rest.Start.Equal(cutoff) || !p.Rest.End.Equal(end) {
		t.Errorf("rest = %+v, want [%v, %v]", p.Rest, cutoff, end)
	}
}

// La frontera exacta: un hueco que EMPIEZA justo en la ventana es recuperable
// entero; un hueco que TERMINA justo en la ventana es bulk entero.
func TestClassifyExactBoundary(t *testing.T) {
	if p := Classify(cutoff, now, now, window); p.Bulk != nil || p.Rest == nil {
		t.Errorf("gap starting exactly at cutoff must be all-REST: %+v", p)
	}
	if p := Classify(now.Add(-80*time.Hour), cutoff, now, window); p.Rest != nil || p.Bulk == nil {
		t.Errorf("gap ending exactly at cutoff must be all-bulk: %+v", p)
	}
}

// fakeSource pagina como la API real: lotes de `pageSize` desde el id pedido.
type fakeSource struct {
	trades   []candle.AggTrade // ordenados por ID
	pageSize int
	calls    int
}

func (f *fakeSource) AggTradesFrom(_ context.Context, fromID int64) ([]candle.AggTrade, error) {
	f.calls++
	var out []candle.AggTrade
	for _, t := range f.trades {
		if t.ID >= fromID {
			out = append(out, t)
			if len(out) == f.pageSize {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeSource) AggTradesSince(_ context.Context, sinceMs int64) ([]candle.AggTrade, error) {
	f.calls++
	var out []candle.AggTrade
	for _, t := range f.trades {
		if t.T >= sinceMs {
			out = append(out, t)
			if len(out) == f.pageSize {
				break
			}
		}
	}
	return out, nil
}

func mkTrades(fromID, toID int64) []candle.AggTrade {
	var out []candle.AggTrade
	for id := fromID; id <= toID; id++ {
		out = append(out, candle.AggTrade{ID: id, T: id * 10, Price: 1, Qty: 1, FirstTradeID: id, LastTradeID: id})
	}
	return out
}

func TestFetchByIDPaginatesAndStopsInclusive(t *testing.T) {
	src := &fakeSource{trades: mkTrades(100, 350), pageSize: 100}
	var got []int64
	if err := FetchByID(context.Background(), src, 120, 305, func(tr candle.AggTrade) error {
		got = append(got, tr.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 186 || got[0] != 120 || got[len(got)-1] != 305 {
		t.Fatalf("got %d ids, first=%d last=%d", len(got), got[0], got[len(got)-1])
	}
	if src.calls < 2 {
		t.Errorf("expected pagination, got %d calls", src.calls)
	}
}

func TestFetchByIDErrorsOnEmpty(t *testing.T) {
	src := &fakeSource{trades: nil, pageSize: 100}
	err := FetchByID(context.Background(), src, 1, 10, func(candle.AggTrade) error { return nil })
	if err == nil {
		t.Fatal("expected error when REST returns nothing for a pending gap")
	}
}

func TestFetchSinceSeedsThenPaginates(t *testing.T) {
	src := &fakeSource{trades: mkTrades(100, 400), pageSize: 100}
	var got []int64
	if err := FetchSince(context.Background(), src, 150*10, 380, func(tr candle.AggTrade) error {
		got = append(got, tr.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got[0] != 150 || got[len(got)-1] != 380 {
		t.Fatalf("first=%d last=%d", got[0], got[len(got)-1])
	}
}
