package backfill

import (
	"context"
	"log/slog"
	"time"

	"jputellas.dev/btcdash/collector/internal/store"
)

// RefreshCAggs rematerializa TODAS las continuous aggregates sobre un rango,
// por tramos y con progreso vista a vista.
//
// Es la reparación de F2a: una CAgg creada después de un backfill nace vacía
// (WITH NO DATA) y su política automática solo materializa su ventana
// (start_offset, 3-7 días). Todo lo anterior a esa ventana existe en
// candles_1m pero NO se sirve: con materialized_only=false la lectura solo
// une el tramo por encima de la marca de agua, no el hueco de debajo.
func RefreshCAggs(ctx context.Context, pg *store.PG, from, to time.Time, only []string) error {
	want := map[string]bool{}
	for _, v := range only {
		want[v] = true
	}
	for _, ca := range store.CAggs {
		if len(want) > 0 && !want[ca.View] {
			continue
		}
		t0 := time.Now()
		if err := pg.RefreshCAgg(ctx, ca, from, to); err != nil {
			return err
		}
		n, first, last, err := pg.TableRange(ctx, ca.View)
		if err != nil {
			return err
		}
		slog.Info("cagg refrescada", "view", ca.View, "elapsed", time.Since(t0).Round(time.Second),
			"velas", n, "desde", first.Format(time.RFC3339), "hasta", last.Format(time.RFC3339))
	}
	return nil
}
