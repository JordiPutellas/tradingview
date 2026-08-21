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
	Src     string `json:"src"`     // tabla o CAgg de la que lee (cobertura: coverage.go)
	query   string
}

// weekOrigin ancla las semanas en lunes (2018-01-01 fue lunes). También ancla
// 3D/5D para que los buckets sean estables entre peticiones.
const weekOrigin = "'2018-01-01Z'::timestamptz"

// direct: el timeframe ya está materializado, se lee tal cual.
func direct(name string, secs int64, table string) Timeframe {
	return Timeframe{name, secs, table, fmt.Sprintf(
		`SELECT extract(epoch FROM ts)::bigint AS t,
  open/1e8 AS o, high/1e8 AS h, low/1e8 AS l, close/1e8 AS c, volume/1e8 AS v
FROM %s WHERE symbol=$1 AND ts >= to_timestamp($2) AND ts < to_timestamp($3)
ORDER BY ts %%s LIMIT $4`, table)}
}

// agg: el timeframe se agrega al vuelo en la query desde `src`.
func agg(name string, secs int64, interval, src, origin string) Timeframe {
	o := ""
	if origin != "" {
		o = ", " + origin
	}
	return Timeframe{name, secs, src, fmt.Sprintf(
		`SELECT extract(epoch FROM time_bucket('%s', ts%s))::bigint AS t,
  first(open, ts)/1e8 AS o, max(high)/1e8 AS h, min(low)/1e8 AS l,
  last(close, ts)/1e8 AS c, sum(volume)/1e8 AS v
FROM %s WHERE symbol=$1 AND ts >= to_timestamp($2) AND ts < to_timestamp($3)
GROUP BY 1 ORDER BY 1 %%s LIMIT $4`, interval, o, src)}
}

// Timeframes: los 24 de TradingView (RF-5.3).
var Timeframes = []Timeframe{
	direct("1s", 1, "candles_1s"),
	agg("5s", 5, "5 seconds", "candles_1s", ""),
	agg("10s", 10, "10 seconds", "candles_1s", ""),
	agg("15s", 15, "15 seconds", "candles_1s", ""),
	agg("30s", 30, "30 seconds", "candles_1s", ""),
	agg("45s", 45, "45 seconds", "candles_1s", ""),
	direct("1m", 60, "candles_1m"),
	direct("3m", 180, "candles_3m"),
	direct("5m", 300, "candles_5m"),
	direct("15m", 900, "candles_15m"),
	direct("30m", 1800, "candles_30m"),
	agg("45m", 2700, "45 minutes", "candles_1m", ""),
	direct("1h", 3600, "candles_1h"),
	direct("2h", 7200, "candles_2h"),
	agg("3h", 10800, "3 hours", "candles_1m", ""),
	direct("4h", 14400, "candles_4h"),
	direct("6h", 21600, "candles_6h"),
	direct("8h", 28800, "candles_8h"),
	direct("12h", 43200, "candles_12h"),
	direct("1D", 86400, "candles_1d"),
	agg("3D", 3*86400, "3 days", "candles_1d", weekOrigin),
	agg("5D", 5*86400, "5 days", "candles_1d", weekOrigin),
	agg("1W", 7*86400, "7 days", "candles_1d", weekOrigin),
	agg("2W", 14*86400, "14 days", "candles_1d", weekOrigin),
	agg("1M", 30*86400, "1 month", "candles_1d", ""),
	agg("3M", 90*86400, "3 months", "candles_1d", ""),
	agg("6M", 180*86400, "6 months", "candles_1d", ""),
	agg("12M", 365*86400, "1 year", "candles_1d", ""),
}

var tfByName = func() map[string]Timeframe {
	m := map[string]Timeframe{}
	for _, tf := range Timeframes {
		m[tf.Name] = tf
	}
	return m
}()
