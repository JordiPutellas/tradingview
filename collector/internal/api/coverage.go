package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TFCoverage: rango que un timeframe sirve REALMENTE, medido con su propia
// query. Existe por el bug de F2a: seis CAggs servían una semana de histórico
// mientras candles_1m tenía siete años, y nadie se enteró hasta verlo en el
// gráfico. TestTimeframeCoverage lo comprueba para los 24.
type TFCoverage struct {
	TF    string `json:"tf"`
	Src   string `json:"src"`
	First int64  `json:"first"` // epoch de la primera vela servible (0 = ninguna)
	Last  int64  `json:"last"`
}

// Coverage mide los extremos de cada timeframe. No pide "desde 1970": los
// timeframes agregados al vuelo harían GROUP BY sobre la tabla entera. Ancla
// una ventana corta en los extremos de la tabla fuente y pide una vela.
func Coverage(ctx context.Context, pool *pgxpool.Pool, symbol string) ([]TFCoverage, error) {
	out := make([]TFCoverage, 0, len(Timeframes))
	for _, tf := range Timeframes {
		c := TFCoverage{TF: tf.Name, Src: tf.Src}
		firstSrc, lastSrc, err := srcRange(ctx, pool, tf.Src, symbol)
		if err != nil {
			return nil, err
		}
		if firstSrc != 0 {
			if c.First, err = oneBar(ctx, pool, tf, symbol, firstSrc, firstSrc+3*tf.Seconds, "ASC"); err != nil {
				return nil, err
			}
			if c.Last, err = oneBar(ctx, pool, tf, symbol, lastSrc-3*tf.Seconds, lastSrc+tf.Seconds, "DESC"); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// srcRange: primer y último ts de la tabla fuente. Sobre una CAgg con
// materialized_only=false esto recorre la unión (materializado + tiempo
// real), que es exactamente lo que ve el frontend.
func srcRange(ctx context.Context, pool *pgxpool.Pool, src, symbol string) (first, last int64, err error) {
	q := func(order string) (int64, error) {
		var t int64
		err := pool.QueryRow(ctx, fmt.Sprintf(
			`SELECT extract(epoch FROM ts)::bigint FROM %s WHERE symbol=$1 ORDER BY ts %s LIMIT 1`,
			src, order), symbol).Scan(&t)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return t, err
	}
	if first, err = q("ASC"); err != nil || first == 0 {
		return 0, 0, err
	}
	last, err = q("DESC")
	return first, last, err
}

func oneBar(ctx context.Context, pool *pgxpool.Pool, tf Timeframe, symbol string, from, to int64, order string) (int64, error) {
	var t int64
	var o, h, l, c, v float64
	err := pool.QueryRow(ctx, fmt.Sprintf(tf.query, order), symbol, from, to, 1).Scan(&t, &o, &h, &l, &c, &v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return t, err
}
