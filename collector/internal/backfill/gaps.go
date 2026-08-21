package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/store"
)

// ResolveGaps resuelve los huecos `pending_bulk` (irrecuperables por REST,
// trampa 10) reprocesando los días afectados desde los ficheros diarios de
// aggTrades. Un hueco solo pasa a `resolved` cuando TODOS sus días se han
// reprocesado; si algún fichero aún no está publicado, el hueco queda
// pendiente y se reintenta en la próxima ejecución.
func ResolveGaps(ctx context.Context, pg *store.PG, symbol, cacheDir string) error {
	gaps, err := pg.ListGapsByStatus(ctx, symbol, store.GapPendingBulk)
	if err != nil {
		return err
	}
	if len(gaps) == 0 {
		slog.Info("resolve-gaps: no pending_bulk gaps")
		return nil
	}
	for _, g := range gaps {
		firstDay := g.Start.UTC().Truncate(24 * time.Hour)
		lastDay := g.End.UTC().Truncate(24 * time.Hour)
		slog.Info("resolve-gaps: processing gap", "gap_id", g.ID, "start", g.Start, "end", g.End)
		complete := true
		for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
			// Fuerza el reproceso aunque backfill_progress ya tenga el día:
			// el hueco existe precisamente porque faltan datos.
			if err := BackfillDay(ctx, pg, symbol, day, cacheDir); err != nil {
				if errors.Is(err, binance.ErrNotPublished) {
					slog.Warn("resolve-gaps: día sin publicar aún, el hueco queda pendiente",
						"gap_id", g.ID, "day", day.Format("2006-01-02"))
					complete = false
					break
				}
				return fmt.Errorf("resolve-gaps gap %d day %s: %w", g.ID, day.Format("2006-01-02"), err)
			}
		}
		if !complete {
			continue
		}
		if err := pg.RefreshCAggs(ctx, firstDay, lastDay.AddDate(0, 0, 1)); err != nil {
			return err
		}
		if err := pg.UpdateGapStatus(ctx, g.ID, store.GapResolved, "resolved from daily bulk files"); err != nil {
			return err
		}
		slog.Info("resolve-gaps: gap resolved", "gap_id", g.ID)
	}
	return nil
}
