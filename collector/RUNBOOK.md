# RUNBOOK — Colector F1a

Cómo arrancar, probar y operar el colector. Los cuatro criterios de éxito de
F1a están en las secciones 2-5.

## 1. Arranque

```bash
docker compose up --build -d
docker compose logs -f collector
```

El colector aplica las migraciones al arrancar (hypertable `candles_1s` con
`quality`, `data_gaps`, retención 6m + compresión 7d, CAggs 1m/5m/15m/1h/4h/1D)
y se conecta al WS. Logs esperados: `applying migration`, `ws connected`,
y flujo de velas.

Variables útiles (ver `internal/config/config.go`): `RECONCILE_WINDOW` (40h),
`HEALTHCHECK_URL`, `FRESHNESS_MAX` (30s), `WS_BASE_URL`.

> **OJO (trampa 12):** el WS de futures exige la ruta `/market/ws`. La ruta
> antigua `/ws` conecta y NO envía aggTrades — en silencio. El default ya es
> el correcto; no lo cambies sin leer la trampa 12 del README.

## 2. Criterio 1 — Ingiere en tiempo real

```bash
docker compose exec timescaledb psql -U btc -d btc -c \
  "SELECT ts, open, close, volume, trade_count, quality
   FROM candles_1s ORDER BY ts DESC LIMIT 10;"
```

Debe haber velas del segundo actual (menos ~5% de segundos vacíos, F0-V6).
Repite a los 10 s y comprueba que `ts` avanza. Los valores están en punto
fijo 1e8: `open/1e8` = precio en USDT.

Sin Docker (pipeline real contra Binance, sin BD):

```bash
go test -tags live -run TestLiveSmoke -v ./internal/collect/
```

Verifica 45 s de stream real: velas bien formadas y `aggTradeId` contiguo
entre velas. Última ejecución aquí: `44 velas, 50.386 BTC, gaps=0`.

## 3. Criterio 2 — Sobrevive a una desconexión del WS

Simula la caída (equivalente al corte forzoso de 24 h, que llega como cierre
normal y sigue el mismo camino):

```bash
docker network disconnect collector_default collector-collector-1
sleep 30
docker network connect collector_default collector-collector-1
```

En logs: `ws disconnected` → reintentos con backoff exponencial y jitter →
`ws connected` → `gap detected` → `gap reconciled via REST`. El test
automatizado equivalente (servidor WS local que corta en seco) es
`TestWSClientReconnects` en `internal/binance/ws_test.go`.

## 4. Criterio 3 — Detecta y reconcilia un hueco provocado

```bash
docker compose stop collector    # deja un hueco de verdad
sleep 300
docker compose start collector
```

Al arrancar: el colector localiza la última vela, refetch desde su
`first_agg_id` (reconstruye entero el segundo frontera, aunque el apagado
dejara una vela parcial) y pagina por `fromId` hasta empalmar con el directo.

```bash
docker compose exec timescaledb psql -U btc -d btc -c \
  "SELECT id, gap_start, gap_end, status, reason, resolved_at FROM data_gaps ORDER BY id;"
docker compose exec timescaledb psql -U btc -d btc -c \
  "SELECT quality, count(*) FROM candles_1s
   WHERE ts > now() - interval '30 min' GROUP BY quality;"
```

Esperado: el gap `resolved`, y las velas del hueco con `quality='reconciled'`.
Un hueco jamás se cierra sin rastro: si la reconciliación falla queda `open`
con la causa en `reason`.

**Drill de la ventana de 48 h** (sin esperar 2 días): arranca con
`RECONCILE_WINDOW=1m` tras un stop de >2 min. La parte vieja del hueco queda
`pending_bulk` con log `ALERT: gap NOT recoverable via REST` (la resolverá el
job de F1b desde el fichero diario); la reciente se reconcilia. La frontera
está cubierta por test (`TestClassifyExactBoundary`, `TestStartupGapOlderThanWindow`).

## 5. Criterio 4 — Expone su estado de salud

```bash
curl -s localhost:8080/health | jq
```

Campos: `status` (ok/stale — stale devuelve HTTP 503), `ws_connected`,
`data_freshness_seconds` (segundos desde el último aggTrade RECIBIDO — la
métrica anti-zombi: un WS conectado que no entrega datos dispara esto, no el
uptime del proceso), `last_candle_ts`, `buffer_len`, `open_gaps`.

Ping saliente: define `HEALTHCHECK_URL` (healthchecks.io). Solo se emite
mientras el dato está fresco; si el stream se para, el ping cesa y la alerta
externa salta aunque el proceso viva. Con la ventana REST de 48 h, configura
el check con margen: periodo 1-2 min, gracia ≤ 30 min.

## 6. Backfill histórico

```bash
docker compose run --rm collector backfill -from 2026-08-01 -to 2026-08-19
```

Descarga los ZIP diarios de data.binance.vision (verificando `.CHECKSUM`),
agrega e inserta con upserts. Idempotente y reanudable: los días completados
se registran en `backfill_progress` y se saltan; interrumpir y relanzar es
seguro. Al final refresca los continuous aggregates del rango.

Nota: el backfill de aggTrades produce `quality='realtime'` (misma fidelidad
que el directo, con el sesgo de frontera de la trampa 9). `exact_t1` queda
reservado al job T+1 de F1b.

## 7. Estado de verificación en este entorno

| Prueba | Estado |
| --- | --- |
| Tests unitarios y de integración (`go test ./...`) | ✅ pasan (agregación, Classify+frontera 40h, hueco vivo reconciliado, idempotencia, pending_bulk, reconexión WS real) |
| Smoke test contra Binance real (WS `/market/ws` + agregación) | ✅ 44 velas/45s, ids contiguos, 0 gaps |
| `docker compose up` end-to-end con TimescaleDB | ⚠️ **pendiente**: Docker Desktop no tiene la integración WSL activada en esta distro y no pudo arrancarse desde la sesión. Los pasos de las secciones 1-5 quedan listos para ejecutarse en cuanto se active (Docker Desktop → Settings → Resources → WSL integration). |

## 8. Limitaciones conocidas (F1a)

- Si la BD cae y el buffer (262k velas ≈ 3 días) se llena, se descartan las
  velas más antiguas con log de error; el hueco resultante lo detecta la
  reconciliación del siguiente arranque. No hay pérdida silenciosa, pero sí
  posible pérdida con ruido si nadie atiende la alerta en días.
- Durante una reconciliación larga los trades vivos se encolan (64k) y el WS
  puede llegar a desconectarse por backpressure; se recupera solo por el
  mismo mecanismo de huecos.
- `trade_count` hereda la sobrecuenta ~0,08% de los IDs quemados por STP
  (trampa 12): nunca usarlo para reconciliación.
