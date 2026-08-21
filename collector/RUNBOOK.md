# RUNBOOK — Colector F1a

Cómo arrancar, probar y operar el colector. Los cuatro criterios de éxito de
F1a están en las secciones 2-5, **verificados en el VPS `jordios` el
2026-08-20** (evidencia en cada sección).

## 0. Particularidades del despliegue en jordios

- Servidor compartido con Hermes (4 GB): TimescaleDB con `mem_limit: 768m` y
  `shared_buffers=128MB`; colector con `mem_limit: 256m`.
- **5432 ocupado** por `jordios-postgres` (Hermes) → TimescaleDB en **5433**.
- **8080 ocupado** por el placeholder de cloudflared → health en **8081**.
- Todo `ports:` con bind explícito `127.0.0.1` (Docker se salta UFW).
- Ruta en el servidor: `~/btcdash/collector`. Deploy: `rsync` desde el repo y
  `docker compose up -d --build`.

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

> **OJO (trampa 11):** el WS de futures exige la ruta `/market/ws`. La ruta
> antigua `/ws` conecta y NO envía aggTrades — en silencio. El default ya es
> el correcto; no lo cambies sin leer la trampa 11 del README.

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
sleep 180   # >90s: cortes menores los absorbe TCP sin desconexión (verificado)
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
curl -s localhost:8081/health | jq   # en jordios; en local, el puerto que mapee el compose
```

Campos: `status` (ok/stale — stale devuelve HTTP 503), `ws_connected`,
`data_freshness_seconds` (segundos desde el último aggTrade RECIBIDO — la
métrica anti-zombi: un WS conectado que no entrega datos dispara esto, no el
uptime del proceso), `last_candle_ts`, `buffer_len`, `open_gaps`.

### Alta en healthchecks.io (pendiente — pasos exactos)

El colector ya emite pings si `HEALTHCHECK_URL` está definida, y funciona
igual sin fallar si está vacía (estado actual). Cuando crees la cuenta:

1. Crea un check en healthchecks.io llamado `btcdash-collector`.
2. Configuración recomendada: **Period = 1 minuto, Grace = 10 minutos**.
   El colector pinga cada minuto SOLO si el dato está fresco (<30 s desde el
   último aggTrade); si el stream se para —aunque el proceso viva, trampas 5 y 11—
   el ping cesa y la alerta salta a los ~11 min. Margen de sobra dentro de la
   ventana REST de 48 h.
3. Copia la URL del check (`https://hc-ping.com/<uuid>`) en
   `docker-compose.yml` → servicio `collector` → `HEALTHCHECK_URL`.
4. En el servidor: `cd ~/btcdash/collector && docker compose up -d collector`
   (recrea el contenedor con la variable).
5. Verifica en healthchecks.io que llegan pings, y prueba la alerta parando
   el colector 15 min (`docker compose stop collector` … `start`).

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

## 7. Estado de verificación (VPS jordios, 2026-08-20)

| Prueba | Estado |
| --- | --- |
| Tests unitarios y de integración (`go test ./...`) | ✅ pasan (agregación, Classify+frontera 40h, hueco vivo reconciliado, idempotencia, pending_bulk, reconexión WS real) |
| Smoke test contra Binance real (WS `/market/ws` + agregación) | ✅ 44 velas/45s, ids contiguos, 0 gaps |
| Migraciones contra TimescaleDB 2.17.2 real | ✅ sin errores a la primera; reejecución idempotente (0 reaplicaciones) |
| Retención: 1s cae, 1m sobrevive | ✅ demostrado con datos sintéticos: `run_job(1000)` borró el chunk viejo de `candles_1s` (180→0 filas) y `candles_1m` conservó sus 3 filas; `candles_5m` las siguió agregando desde 1m |
| Drill arranque limpio | ✅ `up -d` → migraciones → `ws connected` → velas en BD en <30 s |
| Drill ingesta en tiempo real | ✅ 43 velas 1s en 43 s, precios reales, rollup 1m funcionando (job SQL cada minuto) |
| Drill desconexión forzada del WS | ✅ corte de red de 3 min: idle-timeout 90 s → backoff 16/32/64 s → reconexión → hueco de 3.082 aggTrades reconciliado en 3,3 s, 181 velas `reconciled`, gap `resolved`. NOTA: un corte <90 s lo absorbe TCP sin pérdida ni reconexión (verificado con un corte de 34 s) |
| Drill hueco provocado (stop 4 min) | ✅ apagado ordenado (última vela = último segundo antes del stop), rearranque con gap `reason='restart'` de 12.381 aggTrades reconciliado en 11 s; segundo frontera reconstruido completo desde `first_agg_id` |

## 6b. Operaciones F1b: histórico y corrección T+1

Comandos (todos idempotentes y reanudables; progreso en `backfill_progress` y
`job_progress`):

```bash
# 1m oficial desde el origen del par (2019-09-08): meses completos desde el
# ZIP mensual (2020-01+), 2019 y el mes en curso vía REST. Puede correr en
# caliente. quality='official'; el rollup desde 1s nunca las pisa.
docker compose run --rm collector backfill-1m

# 1s desde aggTrades diarios. PARAR EL COLECTOR ANTES (RUNBOOK §8:
# backpressure). Procesa y borra día a día; aborta si el disco baja del
# umbral (-min-free-gb, por defecto 5).
docker compose stop collector
nohup docker compose run --rm collector backfill -from 2024-08-21 -to 2026-08-20 > ~/btcdash/backfill-1s.log 2>&1 &
# ... al terminar:
docker compose start collector   # el hueco se reconcilia solo por REST (<40h)

# Corrección T+1 (diaria por cron, ver abajo). Manual:
docker compose run --rm collector t1                    # continúa hasta ayer
docker compose run --rm collector t1 -from X -to Y      # rango arbitrario (retroactivo bajo demanda)

# Huecos pending_bulk desde ficheros diarios:
docker compose run --rm collector resolve-gaps
```

**Cron instalado** (usuario `jordi`, servidor en UTC): `t1` + `resolve-gaps`
diario a las **09:40 UTC** (los ficheros se publican ~08:00-09:00; si aún no
están, `t1` sale limpio y recupera el día al día siguiente). Log:
`~/btcdash/t1.log`. Ver: `crontab -l`.

El log del T+1 reporta por día: velas corregidas, % de segundos con volumen
desplazado (esperado ~15-20% por el sesgo de frontera, F0), desviación máxima
y media. Si esos números se disparan, algo ha cambiado en el dato de origen.

## 7b. Retención y purga manual

Desde la migración 003 la retención de `candles_1s` es **infinita** (decisión
2026-08-20: comprimir sí, borrar no). La compresión a los 7 días sigue activa.
Verificado en el VPS: cero `policy_retention` en `timescaledb_information.jobs`.

Coste estimado: ~2,7 GB/año de velas 1s sin comprimir (F0), bastante menos
tras compresión. Disco libre en jordios tras el despliegue (imágenes
incluidas): **24 GB** → margen para muchos años. Vigilancia: `df -h /` y
`SELECT hypertable_size('candles_1s');` de vez en cuando.

Si algún día hiciera falta purgar 1s antiguo (la capa 1m+ NO se ve afectada,
está demostrado con prueba real):

```sql
-- borra chunks de candles_1s anteriores a la fecha dada; irreversible
SELECT drop_chunks('candles_1s', older_than => '2027-01-01'::timestamptz);
```

## 7c. Reinicio de kernel PENDIENTE

El VPS corre el kernel 6.8.0-137 con el 138 ya instalado: hay un reinicio
pendiente. **No se ha reiniciado** (decisión: lo hace el usuario cuando le
convenga, Hermes también se ve afectado). Checklist post-reinicio:

1. `ssh jordios` entra (Tailscale arriba).
2. Hermes: `docker ps` muestra `jordios-postgres` healthy y su agente activo.
3. cloudflared: `systemctl status cloudflared` activo (túnel al placeholder).
4. Colector: los dos contenedores `collector-*` arriba solos
   (`restart: unless-stopped` + docker.service habilitado — verificado),
   `curl -s 127.0.0.1:8081/health` → `status: ok`, y en `data_gaps` debe
   aparecer un hueco `restart` resuelto cubriendo el reinicio.

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
