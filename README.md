# Dashboard BTC self-hosted — Requisitos y arquitectura

**Proyecto:** sustituto personal de TradingView Premium para gráficos de Bitcoin
**Dominio:** jputellas.dev
**Usuario:** 1 (privado, con autenticación)
**Fecha:** agosto 2026

---

## 1. Objetivo y alcance

Dashboard de gráficos de Bitcoin auto-alojado, de uso personal, orientado al **análisis del propio trading** con **alta fidelidad en temporalidades bajas (1s–30s)**.

### Dentro de alcance (Fase 1)

- Gráfico de velas de BTC, timeframes de 1s a 1M (incluidos no estándar: 10s, 45m, 3h)
- Herramientas de dibujo persistentes
- Streaming en tiempo real
- Escritorio

### Fuera de alcance (Fase 1, se evalúa después)

- Indicadores (uno a uno, según utilidad real)
- Volume Profile / VPVR
- CVD / delta / footprint
- Alertas server-side + Telegram
- Bar replay
- Móvil

### No objetivos (nunca)

- Multi-activo más allá de BTC y derivados directos
- Pine Script o lenguaje de scripting propio
- Servicio público o multiusuario

---

## 2. Decisiones cerradas

| Decisión              | Elección                                     | Motivo                                                                            |
| --------------------- | -------------------------------------------- | --------------------------------------------------------------------------------- |
| Motor gráfico         | **Lightweight Charts v5** (Apache 2.0)       | Sin contrato, panes nativos, hit-testing nativo, plugins de dibujo existentes     |
| Licencia              | Open source puro                             | La Charting Library exige servicio público y tiene 50.000 USD de daños liquidados |
| Instrumento principal | **BTCUSDT perpetuo** (Binance Futures UM)    | Mayor densidad de trades/s → velas de 1-10s más limpias                           |
| Fuente de velas de 1s | **aggTrades reconstruidos**                  | Futures NO tiene klines de 1s nativas (ni stream WS de trades individuales)       |
| Fidelidad de velas 1s | **Dos niveles: `realtime` → `exact_t1`**     | El sesgo de frontera de aggTrades (trampa 9) no es corregible en vivo; el job T+1 de F1b recalcula desde `daily/trades` y deja las velas exactas |
| Validación cruzada    | Klines 1s nativas de **spot**                | Verificar que la reconstrucción es correcta                                       |
| Colector              | **Go**                                       | Estabilidad en proceso 24/7, baja huella, sin sorpresas de GC                     |
| Base de datos         | **TimescaleDB**                              | Continuous aggregates, ecosistema Postgres, compresión nativa                     |
| Hosting               | **Hetzner** (~4-5 €/mes, UE)                 | Oracle Free recortado y con terminación automática desde 18-ago-2026              |
| Exposición            | **Cloudflare Tunnel + Access**               | Gratis, soporta WebSockets, sin abrir puertos                                     |
| Frontend              | ~~Cloudflare Pages~~ → **VPS, servido por la API** (decisión F1c, ver fila "Hosting frontend") | Mismo origen y un solo deploy |
| Retención 1s          | **Infinita** (decisión 2026-08-20)           | Comprimir sí, borrar no: ~2,7 GB/año con 27 GB libres, y el usuario revisa operaciones antiguas. Purga manual documentada en el RUNBOOK por si acaso |
| Estrategia gráfico    | Directo a LWC, **sin prototipo comparativo** | Validado en spike F1c: 82k velas de 1s a 60 FPS de pan/zoom (suelo conservador en headless). Plan B descartado |
| Plugin de dibujo      | ~~lightweight-charts-drawing~~ → **primitives propias** (decisión F3, ver abajo) | El plugin no podía arreglarse desde fuera: su modelo de anclas es incompatible con lo que pedimos |
| Motor de dibujos      | **Propio, sobre `ISeriesPrimitive` de LWC v5** (`web/src/draw/`) | 7 herramientas, no 68. Ver "Por qué se retiró el plugin" |
| Hosting frontend      | **VPS, servido por la propia API** (no Cloudflare Pages) | Mismo origen (sin CORS), una sola app de Access cubre API+frontend, el WS va al mismo host por el túnel existente, y un único deploy |

### Plan B (documentado, no activado)

Si LWC v5 + plugin de dibujo resulta inviable (drag/hit-testing inestable, rendimiento insuficiente con 86.400 velas), migrar a **KLineChart v10** (Apache 2.0), que trae dibujos e indicadores de fábrica. El backend es agnóstico del motor gráfico, así que la migración no toca el pipeline de datos.

**Criterio de activación del plan B:** si tras 2 días de integración el plugin de dibujo no permite crear, arrastrar y persistir una línea horizontal y un rectángulo de forma estable, parar y reevaluar.

### Por qué se retiró el plugin de dibujo (decisión F3)

El usuario reportó que los dibujos "funcionan fatal": solo se ajustan en
vertical, arrastrar en horizontal mueve el gráfico entero, no se pueden
seleccionar para configurarlos y hay círculos permanentes que sobran. El
diagnóstico sobre el código del plugin y el de LWC (ambos con fuentes
disponibles) encontró que **ninguno de los cuatro síntomas es un bug
puntual**: salen de decisiones de diseño que no se pueden corregir desde
fuera.

1. **El arrastre horizontal no puede funcionar.** El plugin registra
   `mousedown/mousemove/mouseup` sobre el contenedor en **fase de burbuja** y
   no llama a `preventDefault` ni a `stopPropagation` en ninguna parte del
   bundle (0 ocurrencias), ni toca `handleScroll`. LWC recibe el mismo evento
   y panea: en su handler, el desplazamiento del **tiempo es incondicional**
   mientras el del **precio se descarta si el eje está en autoescala** (lo
   está por defecto). Como el gráfico se traslada bajo el cursor exactamente
   los mismos píxeles, **la vela bajo el ratón nunca cambia** y el
   `coordinateToTime(x)` del plugin devuelve el mismo tiempo en cada
   movimiento. De ahí el síntoma exacto: el dibujo solo se mueve en vertical
   y el gráfico se va con el ratón.
2. **El modelo de anclas no admite lo que necesitamos.** `coordinateToTime`
   devuelve el tiempo de una **vela** (movimiento cuantizado, imposible
   colocar algo entre dos velas) y `null` fuera de los datos; el guard es
   todo-o-nada, así que si el tiempo es `null` **también se descarta el
   precio**. En el camino de vuelta, `timeToCoordinate` devuelve `null` para
   tiempos que no existen en la escala y cada renderer aborta: **un dibujo
   anclado a la derecha de la última vela simplemente desaparece**.
3. **Los círculos permanentes no son los puntos de control.** Los de control
   sí están condicionados a la selección; lo que se ve siempre es decoración
   incrustada en el renderer de `HorizontalRay` (un `arc()` relleno y una
   flecha, sin opción para desactivarlos) — y nuestra zona de dos niveles son
   dos `HorizontalRay`, o sea cuatro adornos por zona.
4. **No existe arrastrar la figura.** El plugin solo arrastra **un ancla** y
   únicamente si el `mousedown` cae a ≤8 px de ella: deforma, nunca traslada.
   Agarrar una línea de tendencia por el medio solo puede panear el gráfico.

Un motor propio sobre `ISeriesPrimitive` cuesta ~900 líneas para **7
herramientas y la medición**, y resuelve los cuatro puntos de raíz: eventos en
**fase de captura** (LWC no llega a enterarse del gesto), coordenadas por
**índice lógico fraccionario** con extrapolación (se puede dibujar entre velas
y a la derecha de la última), puntos de control **solo al seleccionar**, y
arrastre de la figura entera. El bundle además adelgaza de 388 kB a 186 kB.
Queda una dependencia menos que mantener; el precio es que el pintado y el
hit-test de cada figura son nuestros.

---

## 3. Arquitectura

```
┌─────────────────────────────────────────────────────────┐
│  Binance                                                 │
│  ├── WS futures: BTCUSDT@aggTrade        (tiempo real)  │
│  ├── WS spot:    BTCUSDT@kline_1s        (validación)   │
│  ├── REST:       /fapi/v1/aggTrades      (gaps)         │
│  └── data.binance.vision                 (backfill)     │
└────────────────────────┬────────────────────────────────┘
                         │
              ┌──────────▼──────────┐
              │  COLECTOR (Go)      │
              │  - WS + reconexión  │
              │  - aggTrades → 1s   │
              │  - detección gaps   │
              │  - reconciliación   │
              └──────────┬──────────┘
                         │
              ┌──────────▼──────────┐
              │  TimescaleDB        │
              │  candles_1s (base)  │
              │  + continuous aggs  │
              │  agg_trades (temp)  │
              └──────────┬──────────┘
                         │
              ┌──────────▼──────────┐
              │  API (Go)           │
              │  REST: histórico    │
              │  WS:   tiempo real  │
              └──────────┬──────────┘
                         │
                 Cloudflare Tunnel
                         │
              ┌──────────▼──────────┐
              │  FRONTEND           │
              │  Lightweight Charts │
              │  + drawing plugin   │
              └─────────────────────┘
```

---

## 4. Requisitos funcionales

### RF-1 — Ingesta de datos

- **RF-1.1** Conexión WebSocket a `btcusdt@aggTrade` de Binance Futures UM, 24/7
- **RF-1.2** Reconexión automática con backoff exponencial (la conexión se corta forzosamente a las 24 h)
- **RF-1.3** Tras cada reconexión, reconciliar el hueco vía REST antes de reanudar la escritura normal
- **RF-1.4** Responder a los `ping` del servidor dentro de la ventana o el stream se cae
- **RF-1.5** Backfill histórico desde `data.binance.vision`, verificando el `.CHECKSUM` de cada ZIP

### RF-2 — Construcción de velas

- **RF-2.1** Agregar aggTrades a velas de 1s: OHLC + volumen + nº de trades + volumen comprador/vendedor (`isBuyerMaker`)
- **RF-2.2** **Paginar SIEMPRE por `fromId`**, nunca por `startTime = últimoT + 1` (se pierden trades del mismo milisegundo)
- **RF-2.3** Los segundos sin trades no generan vela (no rellenar con velas vacías; decidir el comportamiento visual en el frontend)
- **RF-2.4** Timeframes superiores derivados por agregación desde 1s, anclados a **medianoche UTC**
- **RF-2.5** Timeframes no estándar (10s, 45m, 3h) calculados al vuelo. `candles_1m` es **tabla real** (no CAgg: F1b la sobreescribe con klines oficiales, RF-7.2, y una CAgg no admite escritura), alimentada por rollup desde 1s; los comunes ≥5m (5m, 15m, 1h, 4h, 1D) son continuous aggregates **derivadas de `candles_1m`**, para que la corrección de F1b se propague hacia arriba sola

### RF-3 — Validación de integridad

- **RF-3.1** Comparar velas de 1s reconstruidas contra klines 1s nativas de **spot** (mismo periodo) y contra la kline 1m oficial de **futures** (suma de 60 velas de 1s = 1 vela de 1m). F0 midió el techo alcanzable: el volumen diario cuadra al satoshi, pero ~4% de minutos difieren por el sesgo de frontera (trampa 9)
- **RF-3.2** Detectar huecos por discontinuidad de `aggTradeId`, no solo por tiempo
- **RF-3.3** La reconciliación de integridad se basa SIEMPRE en **volumen** (comparación exacta en punto fijo) y en **continuidad de `aggTradeId`** (exacta). NUNCA en conteo de trades: los IDs quemados por STP hacen que `sum(l-f+1)` sobrecuente ~0,08%/día (trampa 12). Excepción: las filas `exact_t1` con `first_agg_id=0` (segundos añadidos por el T+1 desde trades individuales) se verifican solo por volumen — excluirlas de todo chequeo de continuidad
- **RF-3.4** Job periódico de reconciliación contra REST, dentro de la ventana de 48 h (trampa 10)

### RF-4 — API

- **RF-4.1** REST: velas por símbolo + timeframe + rango temporal, con paginación
- **RF-4.2** WS: push de la vela en curso y de las velas cerradas
- **RF-4.3** Persistencia de dibujos (CRUD por símbolo)

### RF-5 — Frontend

- **RF-5.1** Gráfico de velas con LWC v5, con `series.update()` en streaming (nunca `setData()`)
- **RF-5.2** Lazy-loading del histórico por rango visible (no cargar meses de 1s de golpe)
- **RF-5.3** Selector de timeframes — los 24 de TradingView: 1s, 5s, 10s, 15s, 30s, 45s, 1m, 3m, 5m, 15m, 30m, 45m, 1h, 2h, 3h, 4h, 6h, 8h, 12h, 1D, 3D, 5D, 1S, 2S, 1M, 3M, 6M, 12M
- **RF-5.4** Herramientas de dibujo mínimas: línea horizontal, línea de tendencia, rectángulo/zona, texto, medición
- **RF-5.5** Los dibujos persisten al cambiar de timeframe y entre sesiones
- **RF-5.6** Atribución a TradingView visible (`attributionLogo: true`) — obligación de la licencia Apache 2.0
- **RF-5.7** (F2a/F2b) Paleta: velas alcistas `#7092be`, bajistas `#dadada`, fondo `#363636`; cuerpo, borde y mecha del mismo color (sin outline). Sin rejilla, crosshair sólido de 1 px en `#1e1e1e`, y sin indicador de volumen en el gráfico
- **RF-5.8** (F2a) La barra de timeframes ocupa **una sola línea** siempre; lo que no cabe se desplaza lateralmente (rueda y arrastre), nunca se parte en dos filas
- **RF-5.9** (F2a) La barra de herramientas de dibujo es **flotante y arrastrable** (puede superponerse a la de timeframes) y su posición persiste entre sesiones
- **RF-5.10** (F2a) El autoajuste de la escala de precio mira **solo las velas**: los dibujos no lo estiran (se anula `autoscaleInfo` de cada primitive)
- **RF-5.11** (F2a/F2b) Sensibilidad de la rueda y márgenes de la escala **configurables** (`CONFIG` en `web/src/app.js`, ajustable en caliente desde la consola con `cfg.set()` / `cfg.reset()`)
- **RF-5.12** (F2b) El bundle se sirve con hash de contenido en el nombre (`app.<hash>.js`) y el HTML con `Cache-Control: no-cache`: un deploy se ve sin recargas forzadas ni ventanas de incógnito
- **RF-5.13** (F3) **Arrastrar un dibujo lo mueve a él, en los dos ejes, y NUNCA al gráfico.** Click encima selecciona (halo + puntos de control), click fuera deselecciona, Supr borra. Sin puntos de control permanentes: solo con la figura seleccionada
- **RF-5.14** (F3) Panel flotante junto a la figura seleccionada con color, grosor, transparencia, color y transparencia de relleno (donde aplique) y estilo de línea (sólida/discontinua); los cambios se ven en vivo y se persisten
- **RF-5.15** (F3) Herramienta de medición estilo TradingView: Shift+click fija el origen, la medición sigue al cursor, un click la fija y otro la elimina. Muestra diferencia de precio, porcentaje, número de barras y duración (`10d 20h`), con rectángulo semitransparente, flechas y etiqueta
- **RF-5.16** (F3) Las 7 herramientas: horizontal (con y sin extensión), tendencia, rectángulo/zona, curva, arco, punto, texto — más la zona de dos niveles (dos horizontales vinculadas que se mueven juntas; con Shift se ajusta una sola)

### RF-6 — Operación

- **RF-6.1** Autenticación vía Cloudflare Access
- **RF-6.2** Monitorización: ping periódico del colector a healthchecks.io
- **RF-6.3** **[CRÍTICO]** Alerta específica de "última vela recibida hace > N segundos" (detecta streams zombis: conexión viva, datos parados). Con la ventana REST de 48 h (trampa 10), un colector caído sin que nadie se entere durante >48 h deja un agujero irrecuperable por REST hasta el bulk del día siguiente; esta alerta es la que protege esa ventana
- **RF-6.4** Backup periódico de la BD a almacenamiento de objetos

### RF-7 — Corrección batch de fidelidad (F1b, NO en el colector)

- **RF-7.1** Job T+1 que recalcula las velas 1s del día anterior desde `futures/um/daily/trades` (trades individuales, con CHECKSUM) y sobreescribe las velas aproximadas marcándolas `quality='exact_t1'`. F0 demostró que las klines oficiales se reconstruyen exactas 1440/1440 desde ese fichero; coste ~41 MB/día. **Política retroactiva (decidida en F1b): solo hacia adelante por defecto** — corregir los 2 años backfilleados costaría ~30 GB de descarga y horas de proceso para un sesgo cosmético (el volumen diario cuadra al satoshi; solo se desplaza entre segundos adyacentes); `t1 -from -to` acepta cualquier rango si algún día hace falta
- **RF-7.2** Los timeframes ≥1m se sobreescriben con las klines oficiales de data.binance.vision: pasa de 96,7% de minutos exactos (límite del dato aggTrade) a 100%, gratis. Las filas `official` llevan `first/last_agg_id` y `agg_count` a 0 (las klines no traen aggTradeIds); la trazabilidad de huecos vive en `candles_1s`

---

## 5. Requisitos no funcionales

| ID    | Requisito                                                                                               |
| ----- | ------------------------------------------------------------------------------------------------------- |
| RNF-1 | El colector debe sobrevivir semanas sin reinicio manual                                                 |
| RNF-2 | Latencia del tick en pantalla < 500 ms desde el trade real                                              |
| RNF-3 | Pan/zoom fluido (>30 FPS) con un día completo de velas de 1s (86.400 puntos)                            |
| RNF-4 | Coste de infraestructura ≤ 10 €/mes                                                                     |
| RNF-5 | Cero pérdida de datos silenciosa: todo hueco debe ser detectado y registrado                            |
| RNF-6 | La arquitectura no debe cerrar la puerta a un cliente móvil futuro (API limpia y agnóstica del cliente) |

---

## 6. Modelo de datos (borrador)

```sql
-- Tabla base: velas de 1 segundo
CREATE TABLE candles_1s (
  ts            TIMESTAMPTZ  NOT NULL,   -- SIEMPRE UTC
  symbol        TEXT         NOT NULL,   -- 'BTCUSDT-PERP'
  -- Precios y volúmenes en punto fijo BIGINT escala 1e8 (decisión F1a):
  -- la reconciliación por volumen (RF-3.3) exige comparación exacta, y los
  -- floats no la garantizan. La conversión a decimal es cosa de la API.
  open          BIGINT       NOT NULL,
  high          BIGINT       NOT NULL,
  low           BIGINT       NOT NULL,
  close         BIGINT       NOT NULL,
  volume        BIGINT       NOT NULL,
  buy_volume    BIGINT       NOT NULL,  -- para delta/CVD futuro
  trade_count   BIGINT       NOT NULL,
  first_agg_id  BIGINT       NOT NULL,      -- trazabilidad y detección de gaps
  last_agg_id   BIGINT       NOT NULL,
  quality       TEXT         NOT NULL DEFAULT 'realtime',
    -- 'realtime'   : construida en vivo desde el WS (sesgo de frontera, trampa 9)
    -- 'reconciled' : rellenada/verificada vía REST fromId tras un hueco
    -- 'exact_t1'   : recalculada desde daily/trades por el job T+1 (exacta).
    --   OJO: el T+1 preserva first/last_agg_id de las filas existentes, pero
    --   los segundos NUEVOS que solo existen en trades individuales llevan
    --   agg_id = 0 (los trades no traen aggTradeId). La integridad de las
    --   filas exact_t1 se verifica SOLO por volumen (exacto contra la kline
    --   oficial), NUNCA por continuidad de aggTradeId: el chequeo de
    --   continuidad debe excluir first_agg_id = 0.
  PRIMARY KEY (symbol, ts)
);
SELECT create_hypertable('candles_1s', 'ts');

-- Retención: INFINITA (decisión 2026-08-20; la migración 003 elimina la
-- política de 6 meses de la 001). Compresión a los 7 días sí sigue activa.

-- Registro de huecos detectados
CREATE TABLE data_gaps (
  symbol      TEXT NOT NULL,
  gap_start   TIMESTAMPTZ NOT NULL,
  gap_end     TIMESTAMPTZ NOT NULL,
  detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ,
  reason      TEXT
);

-- Dibujos (coordenadas absolutas: precio + timestamp, NO índices de barra)
CREATE TABLE drawings (
  id         UUID PRIMARY KEY,
  symbol     TEXT NOT NULL,
  payload    JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Sobre `quality`:** F0 demostró que la misma vela de 1s puede tener tres orígenes con fidelidades distintas (vivo, reconciliada por REST, recalculada exacta desde trades individuales). Sin esta columna, el job T+1 sobreescribiría velas sin dejar rastro de qué corrigió, y sería imposible auditar qué partes del histórico son exactas y cuáles aproximadas — o detectar que el job T+1 lleva días sin ejecutarse.

```sql
-- candles_1m: TABLA REAL (decisión F1a, ver motivo en RF-2.5), misma
-- estructura que candles_1s pero con quality 'derived' | 'official'.
-- Alimentada por rollup_candles_1m() (job cada minuto, ventana 3 h; y
-- llamadas explícitas tras reconciliar o backfillear). El rollup NUNCA pisa
-- filas 'official'. Los timeframes >=5m son CAggs sobre candles_1m.
```

**Nota sobre `candles_1m` y superiores:** capa histórica permanente e independiente de `candles_1s`: `candles_1m` es una tabla real sin política de retención y las CAggs ≥3m derivan de ella (verificado con prueba real: al dropar chunks de 1s, la capa 1m+ queda intacta). Aunque hoy la retención de 1s es infinita, esta independencia se mantiene por diseño: si algún día se purga 1s, el histórico 1m+ no se ve afectado. F1b sobreescribirá 1m con klines oficiales (`quality='official'`).

**Qué timeframes se materializan (decisión F1a, migración 004):**

- **Materializados** — `candles_1m` (tabla real) + CAggs: 3m, 5m, 15m, 30m, 1h, 2h, 4h, 6h, 8h, 12h, 1D. Los comunes con rangos visibles largos.
- **Al vuelo desde `candles_1s`** — 5s, 10s, 15s, 30s, 45s: un rango visible de sub-minuto son ≤10k filas; agregar en la query es trivial y evita 5 CAggs con refresh continuo sobre la hypertable más caliente.
- **Al vuelo desde `candles_1m`** — 45m, 3h (no estándar).
- **Reparación y control (F2a):** el histórico de una CAgg no se materializa solo (trampa 13). `store.CAggs` es la lista única que usan el backfill y `collector refresh-caggs`; `TestTimeframeCoverage` comprueba contra la BD real que los 24 timeframes sirven desde 2024-08-21 (los de 1s) y desde 2019-09-08 (los de 1m).
- **Al vuelo desde `candles_1d`** — 3D, 5D, S, 2S, M, 3M, 6M, 12M: un año son 365 filas de 1D; el coste es nulo y los anclajes raros (semana en lunes, meses de calendario) los resuelve la API con `time_bucket(origin/timezone)` en F1c, sin pelearse con las restricciones de anclaje de las CAggs.

---

## 7. Trampas identificadas (leer antes de programar)

1. **`fromId` vs `startTime`** — paginar aggTrades por timestamp pierde trades en el borde del milisegundo. Es _el_ bug que produce velas silenciosamente incorrectas. Siempre `fromId`.
2. **Microsegundos en spot** — desde el 1 de enero de 2025 los timestamps de spot en data.binance.vision están en **microsegundos**; futures sigue en milisegundos. Mezclarlos sin normalizar produce fechas absurdas.
3. **Timezone y DST** — LWC no maneja zonas horarias. Todo se almacena y agrega en **UTC**; el offset de Europe/Madrid se aplica **solo en presentación**. Probar explícitamente los cambios de hora de marzo y octubre.
4. **`setData()` en streaming** — reemplaza todo el dataset y mata el rendimiento. Usar `series.update()`.
5. **Stream zombi** — la conexión WS puede seguir abierta sin recibir datos. El heartbeat de proceso no basta; hay que monitorizar la frescura del último dato.
6. **`1mo`, no `1M`** — la nomenclatura de data.binance.vision para el intervalo mensual.
7. **`forceOrder` incompleto** — el stream de liquidaciones solo emite un snapshot por segundo desde abril 2021; no sirve para volumen total de liquidaciones. Relevante solo si se aborda esa feature.
8. **OI histórico: 30 días** — la API solo devuelve los últimos 30 días de Open Interest. Si se quiere histórico largo, hay que capturarlo desde el día 1.
9. **Sesgo de frontera de aggTrades (F0-H1)** — un aggTrade agrupa los fills de una misma orden taker al mismo precio, y su `T` es el timestamp del **primer** fill (verificado en 939.658/939.658 aggregates multi-trade). En un día real, 256.784 aggregates abarcaron >0 ms y **7.977 cruzaron un borde de segundo** (140 un borde de minuto), desplazando todo su volumen al segundo del primer fill. Efecto medido a 1s: 17% de segundos con volumen desplazado (|dV| medio 0,29 BTC, máx 36,6 BTC), 6,5% con OHLC desviado (medio 0,15 USD, máx 52,8 USD). El volumen diario cuadra al satoshi: se desplaza, no se pierde. **No corregible en tiempo real** (futures no tiene stream WS de trades individuales, solo `@aggTrade`); corregible en batch a T+1 desde `futures/um/daily/trades` (RF-7.1).
10. **Ventana REST de 48 h (F0-H3)** — `/fapi/v1/aggTrades` devuelve `{"code":-4166,"msg":"Search window is restricted to recent 2 days only."}` para datos de hace >2 días, tanto con `startTime` como con `fromId` (verificado empíricamente con ambos). Implicación operacional: un hueco solo es reconciliable por REST durante ~48 h; después hay que esperar al fichero diario de data.binance.vision (disponible ~08:00-09:00 UTC del día siguiente). La monitorización de frescura (RF-6.3) es lo único que protege esa ventana.
11. **Ruta `/market` en el WS de futures (F1a)** — desde el aviso del 2026-03-06, los streams de mercado de futures están enrutados: `wss://fstream.binance.com/market/ws/btcusdt@aggTrade`. La ruta antigua sin enrutar (`/ws/...`) **conecta, acepta el SUBSCRIBE y no envía NADA** para `@aggTrade`, `@markPrice` o `@kline` (solo siguen fluyendo los streams "public" como `@bookTicker`): un stream zombi de manual, indistinguible de un mercado parado si no se monitoriza la frescura. Verificado empíricamente el 2026-08-20.
12. **IDs de trade quemados por STP (F0-H2)** — el Self-Trade Prevention (obligatorio en futures desde dic-2024, modo `EXPIRE_MAKER`) consume IDs de trade sin ejecutar volumen: 19.726 IDs inexistentes en un día real, 4.246 de ellos dentro de rangos `[f,l]` de aggregates. `sum(last_trade_id-first_trade_id+1)` sobrecuenta ~0,08% frente al `count` oficial. La reconciliación de integridad NUNCA debe basarse en conteo de trades: solo volumen (exacto) y continuidad de `aggTradeId` (RF-3.3).
13. **Una CAgg creada después del backfill nace vacía (F2a)** — `materialized_only=false` NO significa "lo que falte se calcula al vuelo": la lectura une el hipertable materializado *por debajo* de la marca de agua con el cálculo en vivo *por encima*, y el hueco de debajo no lo rellena nadie. Una CAgg creada `WITH NO DATA` sobre datos ya existentes solo materializa lo que su política automática refresca (`start_offset`, aquí 3-7 días): sirve **días** de histórico mientras `candles_1m` tiene **años**, sin error, sin log y sin diferencia visible en la query. Pasó con las seis CAggs de la migración 004 (3m, 30m, 2h, 6h, 8h, 12h), creadas después del backfill de F1b, mientras las cinco de la 002 estaban en la lista que refresca el backfill y sí tenían el histórico completo. Reparación: `collector refresh-caggs`. Prevención: una sola lista (`store.CAggs`) que usan el refresco y el backfill, y `TestTimeframeCoverage`, que comprueba el rango real de los 24 timeframes contra la BD.

---

## 8. Fases y estimación

| Fase    | Contenido                                                                                     | Horas         |
| ------- | --------------------------------------------------------------------------------------------- | ------------- |
| **F0**  | ✅ HECHO — Spike de validación (veredicto positivo con caveats; ver `spike/f0/RESULTADOS.md`) | 8-12          |
| **F1a** | Colector Go: WS + reconexión + reconciliación + TimescaleDB                                   | 40-70         |
| **F1b** | Backfill histórico + job T+1 de corrección exacta (RF-7.1) + sobreescritura ≥1m (RF-7.2)      | 12-20         |
| **F1c** | ✅ HECHO — API (REST+WS+dibujos) + frontend LWC completo + despliegue tras el túnel (queda la verificación visual del usuario en btc.jputellas.dev) | 15-25 |
| **F1d** | (absorbida por F1c)                                                                           | —             |
| **F1e** | (absorbida por F1c)                                                                           | —             |
| **F1f** | Infra: Hetzner, Cloudflare Tunnel/Access, backups, monitorización                             | 12-20         |
| **F2a** | ✅ HECHO — Arreglo de las 6 CAggs sin histórico (trampa 13) + ajustes de frontend (paleta, barras, zoom, autoescala) | 4-6 |
| **F2b** | ✅ HECHO — Bundle con hash (la caché tapaba F2a), sin volumen ni rejilla, crosshair fino, y tests que miran los píxeles pintados | 2-3 |
| **F3**  | ✅ HECHO — Motor de dibujos propio sobre primitives de LWC (retirado el plugin), panel de configuración, medición, y suite de gestos reales | 10-16 |
|         | **Total Fase 1**                                                                              | **135-232 h** |

Fases posteriores (indicadores, volume profile, CVD, alertas, replay): 60-120 h según alcance.

---

## 9. Orden de ataque

El principio: **validar primero lo que puede matar el proyecto.**

1. **F0 — Spike de aggTrades → 1s.** Un script, un día de datos, comparación numérica contra las klines oficiales. Si esto no cuadra al 100%, todo lo demás sobra. Es el punto de mayor riesgo y el más barato de comprobar.
2. **Spike de frontend.** Cargar 86.400 velas estáticas en LWC v5, medir FPS con pan/zoom, y crear/arrastrar/persistir una línea horizontal con el plugin. Valida el motor y activa (o no) el plan B.
3. **Colector 24/7 + BD.** Dejarlo corriendo una semana antes de construir nada encima. Que aparezcan los fallos de reconexión ahora y no dentro de tres meses.
4. **API + frontend completo.**
5. **Dibujos y persistencia.**
6. Fases posteriores, feature a feature, según utilidad real.

---

## 10. Referencias

- Lightweight Charts: https://github.com/tradingview/lightweight-charts
- Plugin de dibujo: https://github.com/deepentropy/lightweight-charts-drawing
- Plugin alternativo: https://github.com/difurious/lightweight-charts-line-tools
- Datos históricos: https://github.com/binance/binance-public-data
- Prior art (colector multi-exchange): https://github.com/Tucsky/aggr-server
- Plan B (motor gráfico): KLineChart
