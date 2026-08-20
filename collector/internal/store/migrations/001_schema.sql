-- Esquema base del colector (F1a).
-- Precios y volúmenes en punto fijo BIGINT escala 1e8 (regla F0: nada de floats;
-- la reconciliación por volumen exige comparación exacta).
-- Cada sentencia debe ser idempotente: el runner reejecuta ficheros a medias.

CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS candles_1s (
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
  quality       TEXT        NOT NULL DEFAULT 'realtime'
                CHECK (quality IN ('realtime', 'reconciled', 'exact_t1')),
  PRIMARY KEY (symbol, ts)
);

SELECT create_hypertable('candles_1s', 'ts',
  chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

-- Retención: SOLO candles_1s cae a los 6 meses. Los continuous aggregates
-- (002) materializan sus propios datos y no se ven afectados por el drop.
SELECT add_retention_policy('candles_1s', INTERVAL '6 months', if_not_exists => TRUE);

-- Compresión a los 7 días: deja margen de sobra al job T+1 (F1b) para
-- sobreescribir el día anterior antes de que el chunk se comprima.
ALTER TABLE candles_1s SET (
  timescaledb.compress,
  timescaledb.compress_segmentby = 'symbol',
  timescaledb.compress_orderby = 'ts'
);

SELECT add_compression_policy('candles_1s', INTERVAL '7 days', if_not_exists => TRUE);

-- Registro de huecos. Nunca se borra; un hueco se resuelve o queda
-- pending_bulk para F1b, pero jamás desaparece en silencio (RNF-5).
CREATE TABLE IF NOT EXISTS data_gaps (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  symbol               TEXT        NOT NULL,
  gap_start            TIMESTAMPTZ NOT NULL,
  gap_end              TIMESTAMPTZ NOT NULL,
  first_missing_agg_id BIGINT,
  last_missing_agg_id  BIGINT,
  status               TEXT        NOT NULL DEFAULT 'open'
                       CHECK (status IN ('open', 'reconciling', 'resolved', 'pending_bulk')),
  reason               TEXT,
  detected_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS data_gaps_open_idx ON data_gaps (symbol, status)
  WHERE status IN ('open', 'reconciling', 'pending_bulk');

-- Progreso del backfill: permite reanudar y hace el comando idempotente.
CREATE TABLE IF NOT EXISTS backfill_progress (
  symbol        TEXT   NOT NULL,
  day           DATE   NOT NULL,
  rows_ingested BIGINT NOT NULL,
  candles       BIGINT NOT NULL,
  completed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (symbol, day)
);
