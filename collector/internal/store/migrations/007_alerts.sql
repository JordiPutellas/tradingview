-- Alertas de precio server-side (F5, bloque 3).
--
-- El nivel va en punto fijo BIGINT escala 1e8, igual que las velas: el motor
-- compara enteros contra high/low y nunca floats (regla F0). Un cruce justo en
-- el borde no puede depender de un redondeo.
--
-- El motor vive en su PROPIO proceso (cmd/alerts). El colector no se toca: es
-- la ingesta 24/7 y un POST a Telegram que se cuelgue treinta segundos no
-- puede acercarse a ella.

CREATE TABLE IF NOT EXISTS alerts (
  id            UUID PRIMARY KEY,
  symbol        TEXT        NOT NULL,
  level         BIGINT      NOT NULL CHECK (level > 0),
  direction     TEXT        NOT NULL DEFAULT 'any'
                CHECK (direction IN ('up', 'down', 'any')),
  mode          TEXT        NOT NULL DEFAULT 'once'
                CHECK (mode IN ('once', 'recurring')),
  status        TEXT        NOT NULL DEFAULT 'armed'
                CHECK (status IN ('armed', 'paused', 'done')),
  note          TEXT        NOT NULL DEFAULT '',

  -- Vínculo opcional con un dibujo: una línea horizontal ES un nivel. Al
  -- mover el dibujo, el PUT de /api/drawings arrastra el nivel. ON DELETE SET
  -- NULL: borrar la línea no borra la alerta — desaparecer en silencio sería
  -- peor (misma filosofía que data_gaps).
  drawing_id    UUID        REFERENCES drawings(id) ON DELETE SET NULL,
  drawing_point SMALLINT    NOT NULL DEFAULT 0,   -- la zona de dos niveles tiene DOS

  -- Anti-rebote, de lo fino a lo burdo.
  -- rearm_bps: banda de rearme en puntos básicos (5 bps = 0,05%). Para volver
  -- a disparar al alza hay que SALIR de la banda por abajo: el precio bailando
  -- sobre el nivel no genera un segundo mensaje.
  -- cooldown_sec: suelo temporal duro entre avisos de la misma alerta.
  -- max_per_day: tope de cordura; al pasarlo la alerta se pausa sola.
  rearm_bps     INT         NOT NULL DEFAULT 5   CHECK (rearm_bps BETWEEN 0 AND 1000),
  cooldown_sec  INT         NOT NULL DEFAULT 300 CHECK (cooldown_sec BETWEEN 0 AND 86400),
  max_per_day   INT         NOT NULL DEFAULT 20  CHECK (max_per_day BETWEEN 1 AND 1000),

  -- Estado del disparador (Schmitt de tres posiciones): -1 debajo, 0 dentro de
  -- la banda, +1 encima, NULL sin sembrar. La primera vela SIEMBRA sin
  -- disparar: crear una alerta al precio actual no puede avisar en el acto.
  side          SMALLINT    CHECK (side IN (-1, 0, 1)),
  fired_count   INT         NOT NULL DEFAULT 0,
  last_fired_at TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- El motor solo carga lo que puede disparar.
CREATE INDEX IF NOT EXISTS alerts_armed_idx ON alerts (symbol) WHERE status = 'armed';
CREATE INDEX IF NOT EXISTS alerts_drawing_idx ON alerts (drawing_id) WHERE drawing_id IS NOT NULL;

-- Cola de salida E historial a la vez. Se escribe en la MISMA transacción que
-- el cambio de estado de la alerta y ANTES de hablar con Telegram: si el
-- proceso muere entre el disparo y el envío, el mensaje sigue pendiente y sale
-- al arrancar; y un disparo queda registrado aunque Telegram esté caído. Nada
-- desaparece en silencio (RNF-5).
CREATE TABLE IF NOT EXISTS alert_events (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  alert_id   UUID        REFERENCES alerts(id) ON DELETE SET NULL,
  kind       TEXT        NOT NULL DEFAULT 'cross'
             CHECK (kind IN ('cross', 'test', 'system')),
  -- Copia desnormalizada: el historial sobrevive al borrado de la alerta.
  symbol     TEXT        NOT NULL,
  note       TEXT        NOT NULL DEFAULT '',
  direction  TEXT        CHECK (direction IN ('up', 'down')),
  level      BIGINT,
  price      BIGINT,
  candle_ts  TIMESTAMPTZ,                        -- segundo del cruce (el dato)
  fired_at   TIMESTAMPTZ NOT NULL DEFAULT now(), -- reloj del motor
  delivery   TEXT        NOT NULL DEFAULT 'pending'
             CHECK (delivery IN ('pending', 'sent', 'failed', 'skipped')),
  attempts   INT         NOT NULL DEFAULT 0,
  detail     TEXT        NOT NULL DEFAULT '',
  sent_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS alert_events_hist_idx ON alert_events (symbol, fired_at DESC);
CREATE INDEX IF NOT EXISTS alert_events_outbox_idx ON alert_events (fired_at)
  WHERE delivery IN ('pending', 'failed');

-- Marca de agua del motor: hasta qué segundo se evaluó. Es lo único que
-- permite reanudar sin perder cruces y sin inventárselos.
CREATE TABLE IF NOT EXISTS alert_engine (
  symbol     TEXT        PRIMARY KEY,
  last_ts    TIMESTAMPTZ NOT NULL,
  last_price BIGINT,
  detail     JSONB       NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
