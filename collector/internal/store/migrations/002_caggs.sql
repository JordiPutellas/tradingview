-- Continuous aggregates para los timeframes comunes (RF-2.5).
-- Anclados a medianoche UTC (time_bucket con TIMESTAMPTZ usa UTC por defecto).
-- Sus datos materializados NO caen con la retención de candles_1s: la ventana
-- de refresh (start_offset) queda muy por dentro de los 6 meses retenidos.
-- NOTA F1b: RF-7.2 (sobreescribir >=1m con klines oficiales) probablemente
-- convierta candles_1m en tabla real; para F1a el CAgg es suficiente.

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_1m
WITH (timescaledb.continuous) AS
SELECT time_bucket('1 minute', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1s GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_1m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '1 minute',
  schedule_interval => INTERVAL '1 minute', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_5m
WITH (timescaledb.continuous) AS
SELECT time_bucket('5 minutes', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1s GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_5m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '5 minutes',
  schedule_interval => INTERVAL '5 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_15m
WITH (timescaledb.continuous) AS
SELECT time_bucket('15 minutes', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1s GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_15m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '15 minutes',
  schedule_interval => INTERVAL '15 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_1h
WITH (timescaledb.continuous) AS
SELECT time_bucket('1 hour', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1s GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_1h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '1 hour',
  schedule_interval => INTERVAL '15 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_4h
WITH (timescaledb.continuous) AS
SELECT time_bucket('4 hours', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1s GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_4h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '4 hours',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_1d
WITH (timescaledb.continuous) AS
SELECT time_bucket('1 day', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1s GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_1d',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '1 day',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);
