// Package reconcile clasifica huecos y los recupera vía REST.
//
// Máquina de estados de un hueco (R4):
//
//	detectado ──Classify──> dentro de ventana ──REST fromId──> resolved
//	                └─────> fuera de ventana ─────────────────> pending_bulk (F1b)
//	                └─────> a caballo ──> se parte en los dos anteriores
//
// La ventana (40 h por defecto) va con margen sobre el límite real de la API
// (~48 h, error -4166, trampa 10 del README). Un hueco pending_bulk NUNCA se
// cierra aquí: queda registrado para que F1b lo rellene desde el fichero
// diario de data.binance.vision.
package reconcile

import (
	"context"
	"fmt"
	"time"

	"jputellas.dev/btcdash/collector/internal/candle"
)

// TimeRange es un rango [Start, End] en tiempo de exchange.
type TimeRange struct {
	Start, End time.Time
}

// Plan es el resultado de clasificar un hueco.
type Plan struct {
	// Bulk: parte irrecuperable por REST; va a data_gaps como pending_bulk.
	Bulk *TimeRange
	// Rest: parte recuperable por REST ahora mismo.
	Rest *TimeRange
}

// Classify parte un hueco [gapStart, gapEnd] según la ventana REST.
// La frontera exacta (edad == window) se considera recuperable: el margen de
// seguridad ya está en el valor de window (40 h frente a las ~48 h reales).
func Classify(gapStart, gapEnd, now time.Time, window time.Duration) Plan {
	cutoff := now.Add(-window)
	switch {
	case !gapEnd.After(cutoff): // todo demasiado viejo
		return Plan{Bulk: &TimeRange{gapStart, gapEnd}}
	case !gapStart.Before(cutoff): // todo dentro de ventana
		return Plan{Rest: &TimeRange{gapStart, gapEnd}}
	default: // a caballo
		return Plan{
			Bulk: &TimeRange{gapStart, cutoff},
			Rest: &TimeRange{cutoff, gapEnd},
		}
	}
}

// Source es lo que reconcile necesita del cliente REST.
type Source interface {
	AggTradesFrom(ctx context.Context, fromID int64) ([]candle.AggTrade, error)
	AggTradesSince(ctx context.Context, sinceMs int64) ([]candle.AggTrade, error)
}

// FetchByID entrega en orden a fn todos los aggTrades con ID en
// [fromID, untilID], paginando por fromId (RF-2.2: NUNCA por startTime).
func FetchByID(ctx context.Context, src Source, fromID, untilID int64, fn func(candle.AggTrade) error) error {
	cursor := fromID
	for cursor <= untilID {
		batch, err := src.AggTradesFrom(ctx, cursor)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return fmt.Errorf("reconcile: REST devolvió 0 trades desde id=%d con hueco pendiente hasta %d", cursor, untilID)
		}
		for _, t := range batch {
			if t.ID > untilID {
				return nil
			}
			if t.ID < cursor {
				continue
			}
			if err := fn(t); err != nil {
				return err
			}
		}
		cursor = batch[len(batch)-1].ID + 1
	}
	return nil
}

// FetchSince siembra por startTime (solo arranque en frío o tramo tras un
// pending_bulk, donde no hay id de referencia) y continúa por fromId hasta
// untilID inclusive.
func FetchSince(ctx context.Context, src Source, sinceMs int64, untilID int64, fn func(candle.AggTrade) error) error {
	batch, err := src.AggTradesSince(ctx, sinceMs)
	if err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	for _, t := range batch {
		if t.ID > untilID {
			return nil
		}
		if err := fn(t); err != nil {
			return err
		}
	}
	return FetchByID(ctx, src, batch[len(batch)-1].ID+1, untilID, fn)
}
