-- candles_1m y timeframes superiores.
--
-- CAMBIO DE DISEÑO (F1a, verificación en VPS): candles_1m es una TABLA REAL,
-- no una continuous aggregate. Motivo: en F1b se sobreescribirá con las
-- klines oficiales del bulk (RF-7.2, 96,7% -> 100% de exactitud) y una CAgg
-- no admite escritura. Los timeframes >=5m sí son CAggs, pero derivadas de
-- candles_1m: así la corrección de F1b se propaga hacia arriba sola (la
-- invalidación de Timescale se dispara con cualquier escritura en la tabla
-- base, venga del rollup o del job de F1b).
--
-- candles_1m se alimenta desde candles_1s por el job rollup_candles_1m_job
-- (cada minuto, ventana de 3 h) y por llamadas explícitas del colector tras
-- reconciliar un hueco y del backfill tras cada día.

CREATE TABLE IF NOT EXISTS candles_1m (
  ts            TIMESTAMPTZ NOT NULL,
  symbol        TEXT        NOT NULL,
  open          BIGINT      NOT NULL,
  high          BIGINT      NOT NULL,
  low           BIGINT      NOT NULL,
  close         BIGINT      NOT NULL,
  volume        BIGINT      NOT NULL,
  buy_volume    BIGINT      NOT NULL,
  trade_count   BIGINT      NOT NULL,
  agg_count     BIGINT      NOT NULL,
  first_agg_id  BIGINT      NOT NULL,
  last_agg_id   BIGINT      NOT NULL,
  quality       TEXT        NOT NULL DEFAULT 'derived'
                CHECK (quality IN ('derived', 'official')),
  PRIMARY KEY (symbol, ts)
);

SELECT create_hypertable('candles_1m', 'ts',
  chunk_time_interval => INTERVAL '7 days', if_not_exists => TRUE);

-- SIN retención: candles_1m es la capa histórica permanente (README, sec. 6).

-- Rollup 1s -> 1m. NUNCA pisa filas 'official' (las escribirá F1b desde las
-- klines oficiales; el rollup desde aggTrades es menos exacto).
CREATE OR REPLACE FUNCTION rollup_candles_1m(from_ts TIMESTAMPTZ, to_ts TIMESTAMPTZ)
RETURNS void LANGUAGE sql AS $$
  INSERT INTO candles_1m
    (ts, symbol, open, high, low, close, volume, buy_volume, trade_count, agg_count, first_agg_id, last_agg_id)
  SELECT time_bucket('1 minute', ts), symbol,
    first(open, ts), max(high), min(low), last(close, ts),
    sum(volume)::bigint, sum(buy_volume)::bigint,
    sum(trade_count)::bigint, sum(agg_count)::bigint,
    min(first_agg_id), max(last_agg_id)
  FROM candles_1s
  WHERE ts >= from_ts AND ts < to_ts
  GROUP BY 1, 2
  ON CONFLICT (symbol, ts) DO UPDATE SET
    open = EXCLUDED.open, high = EXCLUDED.high, low = EXCLUDED.low,
    close = EXCLUDED.close, volume = EXCLUDED.volume,
    buy_volume = EXCLUDED.buy_volume, trade_count = EXCLUDED.trade_count,
    agg_count = EXCLUDED.agg_count, first_agg_id = EXCLUDED.first_agg_id,
    last_agg_id = EXCLUDED.last_agg_id
  WHERE candles_1m.quality = 'derived';
$$;

CREATE OR REPLACE PROCEDURE rollup_candles_1m_job(job_id INT, config JSONB)
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM rollup_candles_1m(now() - INTERVAL '3 hours', now() + INTERVAL '1 minute');
END;
$$;

SELECT add_job('rollup_candles_1m_job', INTERVAL '1 minute')
WHERE NOT EXISTS (
  SELECT 1 FROM timescaledb_information.jobs WHERE proc_name = 'rollup_candles_1m_job');

-- CAggs >=5m, derivadas de candles_1m. materialized_only=false para que las
-- lecturas incluyan también el tramo aún no materializado.

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_5m
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('5 minutes', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_5m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '5 minutes',
  schedule_interval => INTERVAL '5 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_15m
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('15 minutes', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_15m',
  start_offset => INTERVAL '3 days', end_offset => INTERVAL '15 minutes',
  schedule_interval => INTERVAL '15 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_1h
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('1 hour', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_1h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '1 hour',
  schedule_interval => INTERVAL '15 minutes', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_4h
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('4 hours', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_4h',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '4 hours',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);

CREATE MATERIALIZED VIEW IF NOT EXISTS candles_1d
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('1 day', ts) AS ts, symbol,
  first(open, ts) AS open, max(high) AS high, min(low) AS low, last(close, ts) AS close,
  sum(volume)::bigint AS volume, sum(buy_volume)::bigint AS buy_volume,
  sum(trade_count)::bigint AS trade_count, sum(agg_count)::bigint AS agg_count,
  min(first_agg_id) AS first_agg_id, max(last_agg_id) AS last_agg_id
FROM candles_1m GROUP BY 1, 2 WITH NO DATA;

SELECT add_continuous_aggregate_policy('candles_1d',
  start_offset => INTERVAL '7 days', end_offset => INTERVAL '1 day',
  schedule_interval => INTERVAL '1 hour', if_not_exists => TRUE);
