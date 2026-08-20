-- Decisión 2026-08-20: retención INFINITA en candles_1s.
--
-- Motivo: el usuario revisa operaciones antiguas con frecuencia y un borrado
-- automático podría llevarse justo lo que va a consultar. El coste lo permite:
-- ~2,7 GB/año de velas 1s en crudo (menos comprimido) con ~27 GB libres en el
-- VPS. Comprimir sí (la política de compresión a 7 días sigue activa);
-- borrar, no. Si algún día hace falta purgar, se hace a mano con drop_chunks
-- (ver RUNBOOK, sección "Purga manual").
SELECT remove_retention_policy('candles_1s', if_exists => TRUE);
