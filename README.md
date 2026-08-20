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
| Fuente de velas de 1s | **aggTrades reconstruidos**                  | Futures NO tiene klines de 1s nativas                                             |
| Validación cruzada    | Klines 1s nativas de **spot**                | Verificar que la reconstrucción es correcta                                       |
| Colector              | **Go**                                       | Estabilidad en proceso 24/7, baja huella, sin sorpresas de GC                     |
| Base de datos         | **TimescaleDB**                              | Continuous aggregates, ecosistema Postgres, compresión nativa                     |
| Hosting               | **Hetzner** (~4-5 €/mes, UE)                 | Oracle Free recortado y con terminación automática desde 18-ago-2026              |
| Exposición            | **Cloudflare Tunnel + Access**               | Gratis, soporta WebSockets, sin abrir puertos                                     |
| Frontend              | Cloudflare Pages                             | Estático, gratis                                                                  |
| Retención 1s          | **Últimos N meses** (definir N; sugerido 6)  | El resto en 1m+; lo antiguo es re-descargable de data.binance.vision              |
| Estrategia gráfico    | Directo a LWC, **sin prototipo comparativo** | Plan B documentado, no ejecutado                                                  |

### Plan B (documentado, no activado)

Si LWC v5 + plugin de dibujo resulta inviable (drag/hit-testing inestable, rendimiento insuficiente con 86.400 velas), migrar a **KLineChart v10** (Apache 2.0), que trae dibujos e indicadores de fábrica. El backend es agnóstico del motor gráfico, así que la migración no toca el pipeline de datos.

**Criterio de activación del plan B:** si tras 2 días de integración el plugin de dibujo no permite crear, arrastrar y persistir una línea horizontal y un rectángulo de forma estable, parar y reevaluar.

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
- **RF-2.5** Timeframes no estándar (10s, 45m, 3h) calculados al vuelo; los comunes (1m, 5m, 15m, 1h, 4h, 1D) materializados como continuous aggregates

### RF-3 — Validación de integridad

- **RF-3.1** Comparar velas de 1s reconstruidas contra klines 1s nativas de **spot** (mismo periodo) y contra la kline 1m oficial de **futures** (suma de 60 velas de 1s = 1 vela de 1m)
- **RF-3.2** Detectar huecos por discontinuidad de `aggTradeId`, no solo por tiempo
- **RF-3.3** Job periódico de reconciliación contra REST

### RF-4 — API

- **RF-4.1** REST: velas por símbolo + timeframe + rango temporal, con paginación
- **RF-4.2** WS: push de la vela en curso y de las velas cerradas
- **RF-4.3** Persistencia de dibujos (CRUD por símbolo)

### RF-5 — Frontend

- **RF-5.1** Gráfico de velas con LWC v5, con `series.update()` en streaming (nunca `setData()`)
- **RF-5.2** Lazy-loading del histórico por rango visible (no cargar meses de 1s de golpe)
- **RF-5.3** Selector de timeframes: 1s, 5s, 10s, 15s, 30s, 45s, 1m, 3m, 5m, 15m, 30m, 45m, 1h, 2h, 3h, 4h, 6h, 8h, 12h, 1D, 3D, 5D, 1S, 1M
- **RF-5.4** Herramientas de dibujo mínimas: línea horizontal, línea de tendencia, rectángulo/zona, texto, medición
- **RF-5.5** Los dibujos persisten al cambiar de timeframe y entre sesiones
- **RF-5.6** Atribución a TradingView visible (`attributionLogo: true`) — obligación de la licencia Apache 2.0

### RF-6 — Operación

- **RF-6.1** Autenticación vía Cloudflare Access
- **RF-6.2** Monitorización: ping periódico del colector a healthchecks.io
- **RF-6.3** Alerta específica de "última vela recibida hace > N segundos" (detecta streams zombis: conexión viva, datos parados)
- **RF-6.4** Backup periódico de la BD a almacenamiento de objetos

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
  open          DOUBLE PRECISION NOT NULL,
  high          DOUBLE PRECISION NOT NULL,
  low           DOUBLE PRECISION NOT NULL,
  close         DOUBLE PRECISION NOT NULL,
  volume        DOUBLE PRECISION NOT NULL,
  buy_volume    DOUBLE PRECISION NOT NULL,  -- para delta/CVD futuro
  trade_count   INTEGER      NOT NULL,
  first_agg_id  BIGINT       NOT NULL,      -- trazabilidad y detección de gaps
  last_agg_id   BIGINT       NOT NULL,
  PRIMARY KEY (symbol, ts)
);
SELECT create_hypertable('candles_1s', 'ts');

-- Retención: 1s solo los últimos N meses
SELECT add_retention_policy('candles_1s', INTERVAL '6 months');

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

**Nota crítica sobre `candles_1m` y superiores:** deben tener retención **infinita** (no caen con la política de 6 meses), porque son la capa histórica permanente. Las continuous aggregates se materializan desde `candles_1s` mientras existe; para periodos anteriores a la retención, se rellenan por backfill directo de klines 1m desde data.binance.vision.

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

---

## 8. Fases y estimación

| Fase    | Contenido                                                                                     | Horas         |
| ------- | --------------------------------------------------------------------------------------------- | ------------- |
| **F0**  | Spike de validación: reconstruir 1 día de velas de 1s desde aggTrades y comprobar que cuadran | 8-12          |
| **F1a** | Colector Go: WS + reconexión + reconciliación + TimescaleDB                                   | 40-70         |
| **F1b** | Backfill histórico desde data.binance.vision                                                  | 10-15         |
| **F1c** | API (REST + WS)                                                                               | 15-25         |
| **F1d** | Frontend LWC: velas, timeframes, streaming, lazy-loading                                      | 30-50         |
| **F1e** | Integración del plugin de dibujo + persistencia                                               | 20-40         |
| **F1f** | Infra: Hetzner, Cloudflare Tunnel/Access, backups, monitorización                             | 12-20         |
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
