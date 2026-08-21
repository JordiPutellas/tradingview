// Package api sirve el gráfico: velas por timeframe (REST), streaming (WS),
// dibujos (CRUD) y el frontend estático.
package api

import "fmt"

// Todo en UTC. Los anclajes raros (semana en lunes, meses de calendario) se
// resuelven aquí con time_bucket, como se decidió en F1a; el offset de
// Europe/Madrid es cosa del frontend.
//
// Materializados: candles_1s, candles_1m (tablas), CAggs 3m..1d.
// Al vuelo: sub-minuto desde 1s; 45m/3h desde 1m; >=3D desde 1d.
type Timeframe struct {
	Name    string `json:"name"`
	Seconds int64  `json:"seconds"` // tamaño nominal del bucket (30d para 1M, etc.)
	query   string
}

// weekOrigin ancla las semanas en lunes (2018-01-01 fue lunes). También ancla
// 3D/5D para que los buckets sean estables entre peticiones.
const weekOrigin = "'2018-01-01Z'::timestamptz"

func direct(table string) string {
	return fmt.Sprintf(`SELECT extract(epoch FROM ts)::bigint AS t,
  open/1e8 AS o, high/1e8 AS h, low/1e8 AS l, close/1e8 AS c, volume/1e8 AS v
FROM %s WHERE symbol=$1 AND ts >= to_timestamp($2) AND ts < to_timestamp($3)
ORDER BY ts %%s LIMIT $4`, table)
}

func bucket(interval, src, origin string) string {
	o := ""
	if origin != "" {
		o = ", " + origin
	}
	return fmt.Sprintf(`SELECT extract(epoch FROM time_bucket('%s', ts%s))::bigint AS t,
  first(open, ts)/1e8 AS o, max(high)/1e8 AS h, min(low)/1e8 AS l,
  last(close, ts)/1e8 AS c, sum(volume)/1e8 AS v
FROM %s WHERE symbol=$1 AND ts >= to_timestamp($2) AND ts < to_timestamp($3)
GROUP BY 1 ORDER BY 1 %%s LIMIT $4`, interval, o, src)
}

// Timeframes: los 24 de TradingView (RF-5.3).
var Timeframes = []Timeframe{
	{"1s", 1, direct("candles_1s")},
	{"5s", 5, bucket("5 seconds", "candles_1s", "")},
	{"10s", 10, bucket("10 seconds", "candles_1s", "")},
	{"15s", 15, bucket("15 seconds", "candles_1s", "")},
	{"30s", 30, bucket("30 seconds", "candles_1s", "")},
	{"45s", 45, bucket("45 seconds", "candles_1s", "")},
	{"1m", 60, direct("candles_1m")},
	{"3m", 180, direct("candles_3m")},
	{"5m", 300, direct("candles_5m")},
	{"15m", 900, direct("candles_15m")},
	{"30m", 1800, direct("candles_30m")},
	{"45m", 2700, bucket("45 minutes", "candles_1m", "")},
	{"1h", 3600, direct("candles_1h")},
	{"2h", 7200, direct("candles_2h")},
	{"3h", 10800, bucket("3 hours", "candles_1m", "")},
	{"4h", 14400, direct("candles_4h")},
	{"6h", 21600, direct("candles_6h")},
	{"8h", 28800, direct("candles_8h")},
	{"12h", 43200, direct("candles_12h")},
	{"1D", 86400, direct("candles_1d")},
	{"3D", 3 * 86400, bucket("3 days", "candles_1d", weekOrigin)},
	{"5D", 5 * 86400, bucket("5 days", "candles_1d", weekOrigin)},
	{"1W", 7 * 86400, bucket("7 days", "candles_1d", weekOrigin)},
	{"2W", 14 * 86400, bucket("14 days", "candles_1d", weekOrigin)},
	{"1M", 30 * 86400, bucket("1 month", "candles_1d", "")},
	{"3M", 90 * 86400, bucket("3 months", "candles_1d", "")},
	{"6M", 180 * 86400, bucket("6 months", "candles_1d", "")},
	{"12M", 365 * 86400, bucket("1 year", "candles_1d", "")},
}

var tfByName = func() map[string]Timeframe {
	m := map[string]Timeframe{}
	for _, tf := range Timeframes {
		m[tf.Name] = tf
	}
	return m
}()
