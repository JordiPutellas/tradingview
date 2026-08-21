-- Progreso genérico de jobs batch de F1b (backfill de klines 1m, corrección
-- T+1). backfill_progress (001) queda para el backfill de aggTrades 1s.
CREATE TABLE IF NOT EXISTS job_progress (
  job          TEXT NOT NULL,   -- 'klines1m' | 't1'
  key          TEXT NOT NULL,   -- '2020-01' | '2026-08-20'
  detail       JSONB,
  completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (job, key)
);
