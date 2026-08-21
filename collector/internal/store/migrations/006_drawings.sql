-- Dibujos del gráfico (RF-4.3). Coordenadas SIEMPRE en precio + timestamp
-- UTC absoluto dentro del payload, NUNCA índices de barra: los dibujos deben
-- sobrevivir al cambio de timeframe y a la sesión.
CREATE TABLE IF NOT EXISTS drawings (
  id         UUID PRIMARY KEY,
  symbol     TEXT NOT NULL,
  payload    JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS drawings_symbol_idx ON drawings (symbol);
