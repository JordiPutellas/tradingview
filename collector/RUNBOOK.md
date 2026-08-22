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
- Ruta en el servidor: `~/btcdash/collector`. Deploy: **`./deploy.sh`** desde
  la raíz del repo (rsync + `docker compose up -d --build` + verificación de
  salud); `--web` incluye el frontend. A mano equivale a `rsync -az --delete
  --exclude webdist/ --exclude data/ --exclude .env collector/ jordios:~/btcdash/collector/`.
- **Config específica del servidor SIEMPRE en `~/btcdash/collector/.env`**
  (gitignorado), nunca editando ficheros versionados: el `rsync` del deploy
  los pisa. Lección aprendida en F1b: la `HEALTHCHECK_URL` activada a mano en
  el compose del servidor fue pisada por un deploy y la monitorización quedó
  muda sin que nadie avisara — porque lo que dejó de avisar era el propio
  avisador. Variables actuales en `.env`: `HEALTHCHECK_URL` (y opcionalmente
  `POSTGRES_PASSWORD`).

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
uptime del proceso), `last_candle_ts`, `buffer_len`, `open_gaps`,
`healthcheck_ping` (F2a: `false` = monitorización externa APAGADA; el arranque
lo avisa también en el log con un WARN, y `deploy.sh` lo comprueba tras cada
despliegue).

### Alta en healthchecks.io — ⚠️ APAGADA AHORA MISMO (2026-08-21, F2a)

Montada y probada por el usuario el 2026-08-21 (avisó por Telegram del
reinicio de kernel), pero **el servidor no tiene `~/btcdash/collector/.env`**:
`HEALTHCHECK_URL` está vacía y no se está pingando. La activación se había
hecho editando el compose del servidor y el rsync del deploy volvió a pisarla
(la misma trampa de F1b). Para dejarla viva otra vez, en el servidor:

```bash
printf 'HEALTHCHECK_URL=https://hc-ping.com/<uuid>\n' > ~/btcdash/collector/.env
cd ~/btcdash/collector && docker compose up -d collector
curl -s 127.0.0.1:8081/health | grep -o '"healthcheck_ping":[a-z]*'   # debe decir true
```

`.env` está en `.gitignore` y `deploy.sh` lo excluye del rsync y **aborta** si
no existe o no define `HEALTHCHECK_URL` (sáltatelo a sabiendas con
`ALLOW_NO_HEALTHCHECK=1`). Pasos originales del alta por si hay que recrearla:

1. Crea un check en healthchecks.io llamado `btcdash-collector`.
2. Configuración recomendada: **Period = 1 minuto, Grace = 10 minutos**.
   El colector pinga cada minuto SOLO si el dato está fresco (<30 s desde el
   último aggTrade); si el stream se para —aunque el proceso viva, trampas 5 y 11—
   el ping cesa y la alerta salta a los ~11 min. Margen de sobra dentro de la
   ventana REST de 48 h.
3. Copia la URL del check (`https://hc-ping.com/<uuid>`) en
   `~/btcdash/collector/.env` como `HEALTHCHECK_URL=...` — **nunca** editando
   `docker-compose.yml`, que el deploy pisa (§0).
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

## 6c. API y frontend (F1c)

Servicio `api` en el compose (misma imagen, `entrypoint: ["api"]`), en
`127.0.0.1:8080` — el puerto al que el túnel YA apuntaba, así que cloudflared
no se tocó (la tarea original preveía cambiar su config: no hizo falta, el
ingress ya era `http://127.0.0.1:8080` con IP explícita). El contenedor
`placeholder` quedó parado tras el reinicio de kernel (corría sin política de
restart) y se eliminó.

- Endpoints: `GET /api/candles?tf=&from=&to=&limit=` (filas `[t,o,h,l,c,v]`
  ascendentes, cap 20k, epoch UTC), `GET /api/timeframes`, `GET /api/ws`
  (push de la vela en curso vía LISTEN/NOTIFY del colector, ~3/s),
  CRUD `/api/drawings/{id}`, estáticos del frontend en `/`.
- Deploy del frontend: `./deploy.sh --web` (compila `web/` y sincroniza
  `webdist/`, montado read-only en el contenedor api). El deploy del colector
  NO debe tocar `webdist/` (`--exclude webdist/`).
- Timeframes al vuelo: sub-minuto desde `candles_1s`; 45m/3h desde `candles_1m`;
  ≥3D desde `candles_1d` con `time_bucket` (semanas ancladas a lunes con
  origin 2018-01-01; meses de calendario UTC). El frontend replica los mismos
  anclajes para el streaming (verificado por test).
- **Patrón de lecturas medido** (el punto de RAM abierto en F1b): query típica
  de 20k velas de 1s → 0,40 s; peor caso permitido (45s agregado desde 1s,
  20k barras ≈ 2,6M filas) → 5,8 s con +44 MiB transitorios en TimescaleDB
  (365/768 MiB). Sin riesgo de OOM. Si 45s/sub-minuto se vuelven lentos en el
  uso real, la vía es materializar esas CAggs, no subir RAM.
- Tests del frontend: `cd web && node app-test.mjs` (headless, contra la API;
  requiere `npx playwright install chromium`). Cubre carga, DST
  marzo/octubre Europe/Madrid, anclajes cliente=API, lazy-loading, streaming
  vivo, dibujos (crear/arrastrar/persistir/recargar) y la zona de dos niveles.
- Peculiaridad de LWC: dos clicks casi simultáneos en el mismo píxel se
  absorben (detector de doble click) — irrelevante en uso real, relevante en
  tests sintéticos.
- **Pendiente de verificación del usuario**: WS a través del túnel con
  sesión de Access real (todo lo demás está verificado por dentro; el borde
  responde con el 302 de Access correcto).

## 6d. Operaciones F2a: CAggs sin histórico y despliegue

**El síntoma:** 3m, 30m, 2h, 6h, 8h y 12h servían menos de una semana de velas
mientras el resto llegaba a 2019. **La causa:** esas seis CAggs se crearon en
la migración 004 (2026-08-20 20:51) *después* del backfill, nacen `WITH NO
DATA` y solo su política automática las rellena, que refresca una ventana de
3-7 días. `materialized_only=false` no tapa el hueco: por debajo de la marca de
agua se lee del hipertable materializado, y ahí no había nada (trampa 13 del
README). `store.RefreshCAggs` —lo que llama el backfill— listaba solo las cinco
CAggs de la migración 002, que por eso sí estaban completas.

**Reparación** (idempotente; se puede repetir cuando haga falta):

```bash
cd ~/btcdash/collector
docker compose run --rm collector refresh-caggs -from 2019-09-08          # todas
docker compose run --rm collector refresh-caggs -only candles_6h,candles_12h  # o algunas
docker compose run --rm collector coverage      # rango real de los 24 timeframes
```

Refresca por tramos de 90 días (`store.RefreshChunk`) y recorta el final al
inicio del bucket en curso: materializar un bucket incompleto lo congelaría
hasta el siguiente pase de su política. Coste medido en jordios con 7 años de
`candles_1m` (3,66M filas): **112 s en total** para las seis vistas (3m 62 s y
1.218.726 velas; 30m 14 s; 2h 11 s; 6h 9 s; 8h 8 s; 12h 8 s), con
**TimescaleDB en 636 MiB/768** de pico (casi todo page cache) y el colector
ingiriendo a la vez sin incidencias.

**Control para que no vuelva a pasar en silencio:**

```bash
DATABASE_URL='postgres://btc:...@127.0.0.1:5433/btc' go test ./internal/api -run Coverage -v
```

`TestTimeframeCoverage` mide el primer y el último bucket servible de los 24
timeframes con SU MISMA query y falla si alguno no llega a 2024-08-21 (los que
salen de 1s) o a 2019-10-01 (los que salen de 1m). Se salta solo si no hay
`DATABASE_URL`. Si se añade una CAgg, hay que añadirla a `store.CAggs`: es la
lista que usan el backfill, `refresh-caggs` y nadie más.

**Despliegue con `deploy.sh`** (en la raíz del repo):

```bash
./deploy.sh --web      # rsync + build de la imagen + frontend + verificación
./deploy.sh --no-build # solo sincroniza ficheros
```

Aborta si `~/btcdash/collector/.env` no existe o no define `HEALTHCHECK_URL`
(§0 y §5), excluye `.env`, `webdist/` y `data/` del rsync, y al terminar
comprueba `/health` y `/api/health`, avisando si `healthcheck_ping` es `false`.

**Ajustes de interfaz (F2a/F2b).** Paleta fija en `web/src/app.js`: velas
`#7092be`/`#dadada` con borde y mecha del color del cuerpo, fondo `#363636`,
sin rejilla, crosshair sólido de 1 px en `#1e1e1e` y sin indicador de volumen.
Lo regulable vive en el objeto `CONFIG` y se toca desde la consola del
navegador sin recompilar, con el helper `cfg`:

```js
cfg.list()                   // qué hay en vigor ahora mismo
cfg.set('wheelZoom', 0.25)   // 0.18 por defecto: fracción del rango visible por muesca
cfg.set('priceMarginTop', 0.10)
cfg.reset()                  // borra TODOS los ajustes locales y recarga
```

El zoom de rueda es propio (LWC solo ofrece on/off y mueve un 10% de
`barSpacing` por muesca): escala el rango lógico visible alrededor del cursor,
con topes propios — al fijar el rango a mano nos saltamos los límites de LWC y
alejando sin freno el gráfico acababa empujado fuera de la pantalla. El tope
de alejamiento es "todo lo cargado más 60 velas de margen a cada lado", y como
el histórico crece solo al desplazarse al pasado, se puede seguir alejando.
También se baja `minBarSpacing` a 0,05 (0,5 por defecto): con el valor de
fábrica solo caben ~2.600 velas y, al pedir más, LWC recortaba el ancho de la
ventana pero respetaba su posición.
El autoajuste ignora los dibujos anulando el `autoscaleInfo` de cada primitive
del plugin, que por defecto devuelve el rango de sus anclas. La posición de la
barra de dibujo flotante se guarda en `localStorage['btcdash.toolbarPos']`.

**Dibujos (F3).** Motor propio en `web/src/draw/` sobre la API de primitives
de LWC v5; el plugin `lightweight-charts-drawing` se retiró (el porqué, con
evidencia, en el README). Piezas: `geom.js` (geometría y formateo), `shapes.js`
(catálogo de figuras: pintado, hit-test y puntos de control), `engine.js`
(primitive, eventos, arrastre, medición y persistencia) y `panel.js` (panel de
configuración). Lo que hay que saber para operarlo:

- Los eventos se capturan **en fase de captura** sobre `#chart` y, cuando el
  gesto es nuestro, se corta la propagación: por eso arrastrar un dibujo ya no
  mueve el gráfico. Si alguna vez vuelve a moverse, mirar ahí primero. El
  candado de `handleScroll` se echa solo al empezar un arrastre y se suelta en
  CUALQUIER `pointerup`, `pointercancel` o pérdida de foco: si el gráfico deja
  de panearse, es que ese camino se rompió.
- **Trampa 14 del README**: `logicalToCoordinate` devuelve 0 con índices
  fraccionarios y `coordinateToLogical` redondea. Todo lo que toque
  coordenadas debe pasar por `xOf`/`logicalOfX`, que interpolan.
- Las figuras se guardan en `(tiempo UTC, precio)` y se pintan pasando por el
  **índice lógico fraccionario**, no por `timeToCoordinate`: así se pueden
  colocar entre velas y a la derecha de la última.
- Formato en la API: `{kind:'shape', v:1, type, points:[{t,p}], style, style2?, text?}`
  en `PUT /api/drawings/{id}`. La tabla estaba vacía al hacer el cambio, así
  que no hubo migración; `load()` ignora cualquier payload que no sea `shape`.
- Medición: Shift+click (o el botón 📏) fija el origen, sigue al cursor, un
  click la fija y otro la borra.

⚠️ **Las dos suites trabajan contra la BD REAL** (el API local va por el túnel
a jordios) y **borran la tabla de dibujos** para empezar en limpio. Guardan lo
que encuentran al arrancar y lo reponen al terminar —también si se caen a
media ejecución—, pero el `updated_at` cambia y una interrupción a lo bruto
(Ctrl+C, cierre del terminal) se lleva los dibujos por delante. Si hay algo que
no se quiera perder, sacarlo antes:

```bash
curl -s http://127.0.0.1:8090/api/drawings > /tmp/dibujos.json
```

Tests (los dos necesitan la API en 127.0.0.1:8090 sirviendo `web/dist`):

```bash
cd web && npm test          # app-test.mjs (gráfico) + draw-test.mjs (dibujos)
```

`draw-test.mjs` usa **gestos reales** (mousedown/mousemove/mouseup con
coordenadas) y comprueba efectos: que la figura se mueve en los dos ejes, que
el **rango visible del gráfico no cambia** durante el arrastre, y los píxeles
pintados en el canvas tras cada cambio del panel.

**Navegación, estilos y ergonomía (F4).** Lo que cambió en el manejo diario:

- **El rango temporal visible sobrevive al cambio de timeframe.** Se guarda en
  tiempo UTC absoluto antes de cambiar, se pide ese tramo en el destino y se
  restaura. Decisión sobre el número de barras: **tope de 12.000 velas
  visibles** (`cfg.tfChangeMaxBars`) y **suelo de 20** (`cfg.tfChangeMinBars`).
  Dos años en 1h son ~17.500 velas, pero en 1s serían 60 millones: por encima
  del tope se centra en el mismo instante y se enseñan las que caben, con aviso
  en la barra de estado (`1m · 12000 velas (tope)`); por debajo del suelo se
  ensancha, porque pasar de 1s a 1h conservando cuatro minutos dejaba UNA vela
  en pantalla. El tope de la API sigue siendo 20.000 por petición: la
  diferencia se reparte como margen a los lados (mínimo 200 velas) para poder
  desplazarse un poco sin volver al servidor.
- **Las flechas hacen lo contrario que el click, a propósito.** El click en la
  barra conserva el tramo; las flechas conservan el **ancho de vela**, o sea el
  número de velas a la vista, para que sirvan de zoom: bajar de temporalidad
  acerca y subir aleja. Punto fijo: el borde derecho si se está pegado al
  presente —se sigue viendo la última vela—, y el centro de la pantalla si se
  está mirando el pasado.
- Si el tramo pedido **no existe** en el timeframe de destino (2020 en 1s, que
  empieza en 2024) se cae al comportamiento de siempre —últimas velas, presente—
  avisando con `sin datos en ese tramo`.
- Con la ventana en el pasado, el estado dice `pasado · End vuelve`: el
  streaming NO toca las velas (metería la de ahora detrás de una de 2020) y
  desplazarse hacia la derecha carga velas nuevas. **End** vuelve al presente
  conservando el ancho de la ventana.
- El timeframe y la posición se guardan en `localStorage['btcdash.view']` y se
  restauran al recargar. Para volver al arranque de fábrica:
  `localStorage.removeItem('btcdash.view')`.

Atajos de teclado (todos se apagan dentro de un campo de texto; los de una
sola letra, dentro de cualquier control de formulario):

| Tecla | Efecto |
| --- | --- |
| ↑ / ↓ | timeframe siguiente / anterior, conservando el ancho de vela (acerca al bajar, aleja al subir) |
| End | volver al presente |
| Supr | borrar el dibujo seleccionado |
| Esc | cancelar herramienta, medición o selección |
| Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y | deshacer / rehacer (100 pasos) |
| Ctrl+C / Ctrl+V | duplicar el dibujo seleccionado |
| Alt + arrastrar | duplicar y mover la copia |
| M | imán |
| H | ocultar todos los dibujos |
| L | candado |

Ajustes nuevos de `cfg`: `tfChangeMaxBars` (12000), `tfChangeMinBars` (20),
`magnetPx` (12). Claves de `localStorage` que usa el frontend:
`btcdash.view`, `btcdash.toolbarPos`, `btcdash.estiloActual`,
`btcdash.plantillas`, `btcdash.plantillaDefecto`, `btcdash.iman`,
`btcdash.dibujosOcultos`, `btcdash.dibujosBloqueados`, más los `cfg.*`.

Estilos y plantillas (`web/src/draw/styles.js`): cualquier cambio de estilo se
convierte en el defecto de esa herramienta, y una plantilla con nombre es una
copia guardada de ese mismo estilo. Marcar una plantilla como predeterminada
**borra la memoria por herramienta**: es una orden de "todo lo nuevo así".
Vive en el navegador, no en la base de datos.

Deshacer/rehacer (`web/src/draw/history.js`) guarda estados completos y
reconcilia con el servidor: lo que desaparece se borra y lo que vuelve se
reescribe. Los cambios de estilo se agrupan medio segundo para que un
deslizador no deje treinta pasos.

**Dos cosas que se arreglaron por el camino:**

- `GET /api/candles` sin `from` acotaba el escaneo con `to - limit*bucket*3`.
  Para 12M y 5.000 velas eso son 47.000 años hacia atrás y PostgreSQL tumbaba
  la query con un 500 (`rows failed`). Ahora hay suelo en epoch 0 y techo en
  hoy+10 años; lo cubre `TestDeepHistoryRequest`.
- Trampa 15 del README: LWC guarda la posición como distancia a la ÚLTIMA
  vela, así que cargar velas por la derecha arrastraba la vista (de marzo de
  2020 a diciembre de 2025). Hay que volver a fijar el rango tras cada
  `setData`.

**Limitación conocida:** las velas cargadas se acumulan mientras se pasea por
el histórico (cada página son 5.000 filas más en memoria del navegador).
Cambiar de timeframe o recargar limpia. Con paseos largos en 1s conviene
recargar de vez en cuando.

**Caché del bundle (F2b).** Un deploy no se veía hasta abrir una ventana de
incógnito: el fichero nuevo estaba en el servidor —el `grep` lo confirmaba—
pero el navegador seguía sirviendo su copia de `app.js`, la misma URL de
siempre y sin cabeceras que dijeran nada. Ahora `npm run build` escribe
`app.<hash>.js` e `index.html` apunta a esa URL, así que cada versión es un
fichero distinto; la API sirve el HTML con `Cache-Control: no-cache,
must-revalidate`, los bundles con hash como `immutable` y las respuestas JSON
con `no-store` (sin validadores, un navegador puede cachear un GET por
heurística y servir velas viejas). Para comprobar qué versión está viendo el
navegador basta mirar el `src` del `<script>` en el HTML servido:

```bash
curl -s http://127.0.0.1:8080/ | grep -o 'src="[^"]*"'
```

## 7c. Cifras con el histórico completo cargado (2026-08-21, cierre F1b)

Medición con 2 años de 1s + 7 años de 1m cargados y el colector ingiriendo:

| Métrica | Valor |
| --- | --- |
| `candles_1s` | 61.110.976 filas: 60.993.008 `realtime` + 85.639 `exact_t1` + 32.329 `reconciled` (2024-08-21 → ahora) |
| `candles_1m` | 3.656.057 filas desde 2019-09-08 17:57 (99,99% `official`) |
| Integridad 2 años | **0 discontinuidades de aggTradeId** en 61,05M velas (excluidas 21 filas `agg_id=0` del T+1); cobertura 96,80% de segundos; conteo exacto contra `backfill_progress` |
| CAggs | 0 diferencias contra la base en 1h (mar-2025), 1d (dic-2024) y 5m (día T+1); volumen del día T+1 exacto al satoshi entre 1s y 1m |
| RAM | collector 30,3 MiB/256 · **timescaledb 392,1 MiB/768** (I/O acumulado del backfill: 14,4 GB read / 64,2 GB write) · Hermes intacto |
| Disco | 19 GB usados / **18 GB libres** (53%); la compresión a 7 días seguirá reduciendo los chunks del backfill |
| `data_gaps` | 6 huecos, **todos `resolved`** (incluidas las 9 h de la parada del backfill); 0 `pending_bulk` |
| Salud | `status: ok`, frescura 39 ms, buffer 0 |

**Conclusión: NO escalar el servidor.** Con la carga completa, TimescaleDB usa
la mitad de su `mem_limit` y el sistema conserva >2,5 GiB disponibles y swap
intacto. El backfill (el pico de I/O y RAM de la vida del sistema) ya pasó.
Revisar solo si F1c (API) cambia el patrón de lecturas.

## 7d. Reinicio del servidor — ✅ VERIFICADO (2026-08-21, kernel 6.8.0-138)

El reinicio de kernel se hizo y el checklist pasó entero: los contenedores
arrancaron solos y el hueco del reinicio (gap 7) se reconcilió en 6 s.

**Advertencia aprendida:** `restart: unless-stopped` NO rearranca un
contenedor que estaba PARADO A MANO antes del reinicio (ese es justo el
significado de "unless stopped"). En un reinicio planificado con el colector
parado (p. ej. durante un backfill), tras el boot hay que hacer
`docker compose up -d collector` explícitamente.

Checklist post-reinicio (para la próxima vez):

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
  mismo mecanismo de huecos. **Observado con carga real** (2026-08-21, tras
  las 9 h de parada del backfill de 1s):
  `15:57:09 gap de 2,66M aggTrades reconciliado → 15:57:10 ws disconnected
  ("unexpected EOF" tras 37 min conectado) → 15:57:12 ws connected + gap de
  2 min ("stream discontinuity") → 15:57:16 segundo gap reconciliado`.
  El sistema se recuperó solo de su propio efecto secundario, pero la
  cascada es real, no hipótesis. Recomendación operativa: **no lanzar el
  T+1 (ni otros batch pesados) en paralelo a una reconciliación larga** —
  tras un arranque con horas de hueco, deja que el colector empalme primero.
- `trade_count` hereda la sobrecuenta ~0,08% de los IDs quemados por STP
  (trampa 12): nunca usarlo para reconciliación.
