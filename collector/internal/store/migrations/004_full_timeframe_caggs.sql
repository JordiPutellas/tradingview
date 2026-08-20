-- CAggs para el set completo de timeframes de TradingView (24).
--
-- Qué se materializa y qué no (justificación en README, sección 6):
--   - Materializado (CAggs sobre candles_1m): 3m, 5m, 15m, 30m, 1h, 2h, 4h,
--     6h, 8h, 12h, 1D. Son los comunes con rangos visibles largos.
--   - Al vuelo desde candles_1s: 5s, 10s, 15s, 30s, 45s. Un rango visible de
--     sub-minuto son <=10k filas de 1s: agregar en la query es trivial y nos
--     ahorra 5 CAggs con refresh continuo sobre la hypertable más caliente.
--   - Al vuelo desde candles_1m: 45m, 3h (no estándar).
--   - Al vuelo desde candles_1d: 3D, 5D, S, 2S, M, 3M, 6M, 12M. Un año son
--     365 filas de 1D; agregar semanas/meses al vuelo cuesta cero y evita
--     lidiar con anclajes de CAgg (semana en lunes, meses de calendario) que
--     la API resolverá con time_bucket(origin/timezone) en F1c.
-- Este fichero añade las CAggs que faltaban: 3m, 30m, 2h, 6h, 8h, 12h.

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_3m
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('3 minutes', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_3m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '3 minutes',
  schedule_interval => INTERVAL '3 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_30m
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('30 minutes', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_30m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '30 minutes',
  schedule_interval => INTERVAL '15 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_2h
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('2 hours', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_2h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '2 hours',
  schedule_interval => INTERVAL '30 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_6h
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('6 hours', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_6h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '6 hours',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_8h
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('8 hours', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_8h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '8 hours',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_12h
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('12 hours', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_12h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '12 hours',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);
