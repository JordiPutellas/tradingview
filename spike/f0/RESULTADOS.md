# F0 — Resultados del spike: velas de 1s desde aggTrades (BTCUSDT perp)

**Fecha del análisis:** 2026-08-20
**Día analizado:** 2026-08-19 UTC completo (validación cruzada adicional con 2026-08-18)
**Código:** `spike/f0/` (Go, solo stdlib). Comandos: `validate`, `simulate`, `rest`, `compare-rest`, `diag`, `truth`.

## Veredicto

**SÍ es viable, con caveats acotados y cuantificados.** La reconstrucción desde aggTrades
es *conservadora del volumen* (el día cuadra al satoshi contra las klines oficiales) y
*sin pérdida de trades* (continuidad de `aggTradeId` perfecta). Las discrepancias que
existen son artefactos de frontera temporal inherentes al propio dato aggTrades — no
bugs de nuestra agregación — y están caracterizadas al detalle más abajo. La kline
oficial se reconstruye **exacta 1440/1440** desde trades individuales, lo que nos da un
mecanismo de corrección a posteriori si algún día hace falta exactitud absoluta.

## Datasets y verificación

| Fichero (data.binance.vision) | Filas | CHECKSUM |
| --- | --- | --- |
| futures/um/daily/aggTrades 2026-08-19 | 2.061.378 | sha256 OK |
| futures/um/daily/klines 1m 2026-08-19 | 1.440 | sha256 OK |
| spot/daily/klines 1s 2026-08-19 | 86.400 | sha256 OK |
| futures/um/daily/**trades** 2026-08-19 (ground truth, añadido durante el spike) | 5.388.815 | sha256 OK |
| (los mismos tres primeros para 2026-08-18) | 545.233 / 1.440 / 86.400 | sha256 OK |

Unidades de timestamp confirmadas: futures en **milisegundos**, spot en **microsegundos**
(primera columna `1787097600000000`). El lector normaliza a ms por magnitud
(umbral 1e14) y reporta la unidad detectada. La trampa nº 2 del README es real.

## Decisiones de agregación (explícitas)

1. **Asignación trade→segundo:** `floor(T_ms / 1000)`. Las klines oficiales cierran en
   `.999`, así que el truncado coincide con el bucketing de Binance; no hay ambigüedad
   de borde *a nivel de milisegundo*.
2. **Segundos sin trades:** no generan vela (RF-2.3). El hueco se representa por
   ausencia; `first_agg_id`/`last_agg_id` permiten verificar que no falta nada.
3. **`trade_count`:** cuenta trades individuales (`sum(last_trade_id-first_trade_id+1)`),
   la semántica del campo `count` de las klines oficiales — con el caveat de los IDs
   quemados por STP (ver V3). Se guarda también `agg_count` (filas de aggTrades).
4. **`buy_volume`:** volumen taker-buy (`is_buyer_maker == false`), la misma semántica
   que `taker_buy_volume` de las klines oficiales.
5. **Aritmética:** punto fijo int64 con escala 1e8 (sin floats). Toda comparación de
   este informe es **exacta al último decimal**; no hay tolerancias.

## Tabla resumen V1–V6 (día 2026-08-19)

| Validación | Resultado | Detalle |
| --- | --- | --- |
| V1 · 60×1s vs kline 1m oficial | **744/1440 minutos exactos en los 7 campos** | close: 1440/1440 · high: 1436 · low: 1437 · open: 1343 · volume: 1188 · buy_volume: 1331 · trade_count: 760. Causas caracterizadas abajo. |
| V2 · volumen del día | ✅ **EXACTO al satoshi** | aggTrades 326.496,482 BTC = klines 326.496,482 BTC (dif 0). Taker-buy también exacto: 179.584,584 BTC. |
| V3 · conteo de trades | ⚠️ dif **+4.246** (0,08%) | `sum(l-f+1)`=5.393.061 vs klines 5.388.815. La premisa del enunciado es *casi* cierta: el exceso son IDs quemados por STP (ver hallazgos). |
| V4 · continuidad aggTradeId | ✅ **0 huecos** | 2.061.378 ids consecutivos (3410435258..3412496635), 0 inversiones de tiempo. Ídem el 08-18. |
| V5 · bulk vs REST fromId | ✅ **IDÉNTICOS** | 2.061.378 filas vs 2.061.378; **0 diferencias de contenido** (comparación numérica exacta campo a campo). 2.062 requests, 28m32s. |
| V6 · densidad spot vs perp | **Perp gana con claridad** | Segundos vacíos: perp 5,04% vs spot 16,25%. Media trades/s: 62,4 vs 48,2. p50: 9 vs 3. |

En el día de contraste 2026-08-18 (4× menos actividad): V2 exacto (97.513,888 BTC),
V4 sin huecos, V1 1121/1440 exactos, V3 dif +475. Mismo patrón cualitativo.

## Paginación REST: fromId vs startTime (el bug crítico)

Simulación determinista sobre el fichero bulk (páginas de 1000, cursor `startTime = últimoT+1`):

- **Trades PERDIDOS por paginar por startTime: 14.872 de 2.061.378 (0,72%)**
- 1.256 de ~2.062 bordes de página pierden trades (el 61% de los bordes)
- Ejemplo real: tras un borde de página se pierden 20+ aggTrades consecutivos del
  mismo milisegundo (ids 3410437258..3410437277)

Descarga REST real con ambas estrategias (día completo, 2.062/2.047 requests):

- `fromId`: 2.061.378 trades — **idéntico al bulk al 100%** (V5)
- `startTime = últimoT+1`: 2.046.506 trades — **14.872 perdidos (0,7215%), 0 duplicados, 0 extras**
- La pérdida real coincide **exactamente** con la simulación determinista (14.872):
  el mecanismo está completamente caracterizado, no es ruido de red.

La regla RF-2.2 (paginar SIEMPRE por `fromId`) queda sobradamente justificada: casi 15k
trades/día perdidos en silencio, concentrados justo en los milisegundos de más actividad
(que es cuando una página de 1000 se corta a mitad de ráfaga — el peor momento posible
para perder datos en un gráfico de trading).

## Hallazgos no esperados (importantes para F1)

### H1. La kline oficial se construye desde trades individuales, y aggTrades NO es bit-perfect a 1s

Reconstruyendo las 1440 klines 1m desde el fichero de **trades individuales**
(`futures/um/daily/trades`): **1440/1440 exactas en los 7 campos**. Ground truth
establecido.

Contra esa verdad, nuestras velas 1s desde aggTrades tienen:

- 13.979 de 82.045 segundos (17,0%) con volumen distinto — |dV| máx 36,586 BTC,
  medio 0,29 BTC en los afectados; **suma diaria exacta** (solo se desplaza volumen
  entre segundos adyacentes, nunca se pierde)
- 5.309 segundos (6,5%) con algún campo OHLC distinto — |dPrecio| máx 52,8 USD,
  medio 0,15 USD en los campos afectados
- 47 segundos que existen en la verdad pero no en la reconstrucción

**Causa raíz:** un aggTrade agrupa trades de la misma orden taker al mismo precio, y
su `T` es el timestamp del **primer** trade del grupo (verificado en 939.658/939.658
aggregates multi-trade). 256.784 aggregates abarcan >0 ms; **7.977 cruzan un borde de
segundo** (hasta ~70 ms de span) y 140 cruzan un borde de minuto. Todo el volumen del
aggregate cae en el segundo del primer fill, mientras la kline oficial reparte cada
fill en su segundo real. Esto explica también las discrepancias V1 de open/volume:
en el 08-19, los 252 minutos con volumen distinto cuadran con los 140 cruces de
borde de minuto (cada cruce afecta a 2 minutos).

**Implicación:** este error es inherente al dato aggTrade — el colector WS en vivo
tendrá exactamente el mismo sesgo. No es corregible en tiempo real porque futures
**no ofrece stream WS de trades individuales** (solo `@aggTrade`; verificado en la
documentación oficial). Sí es corregible en batch: el fichero diario
`futures/um/daily/trades` permite recalcular las velas 1s perfectas a T+1.

### H2. IDs de trade quemados por Self-Trade Prevention

En el día hay 19.726 IDs de trade individuales que **no existen** (span 5.408.541 vs
5.388.815 filas): 15.480 caen entre aggregates y **4.246 dentro de rangos `[f,l]`de
aggregates**, y estos últimos inflan `sum(l-f+1)` frente al `count` oficial (+4.246,
exactamente la diferencia de V3). Consistente con el STP obligatorio en futures desde
dic-2024 (modo `EXPIRE_MAKER`: órdenes expiradas consumen IDs sin ejecutar volumen).
Volumen y precios no se ven afectados — solo el conteo.

**Implicación:** `trade_count` reconstruido sobreestima ~0,08%/día. Irrelevante para
velas, relevante si algún día se usa `count` para reconciliación estricta: la
reconciliación de integridad debe hacerse por **volumen** (exacto) y por
**continuidad de aggTradeId** (exacta), no por conteo de trades.

### H3. El REST de aggTrades solo llega 2 días atrás

`GET /fapi/v1/aggTrades` con `startTime` **o** `fromId` de hace >2 días devuelve
`{"code":-4166,"msg":"Search window is restricted to recent 2 days only."}` (verificado
empíricamente con ambos parámetros; por esto el spike usa el 2026-08-19 y no el 18).

Confirmado después en el change log oficial: el lookback de `/fapi/v1/aggTrades` se
amplió de 24 h a 48 h el 2026-08-13 (una semana antes de este spike). Es un límite
documentado y RECIENTE: puede volver a cambiar en cualquier dirección.

**Implicación para F1a/F1b:** la reconciliación de huecos por REST tiene una ventana
dura de ~48 h. Un colector caído más de 2 días ya no puede recuperar por REST; debe
esperar al fichero diario de data.binance.vision (aparece ~08:00-09:00 UTC del día
siguiente — observado en los mtimes de los ficheros). La ventana está cubierta, pero
el runbook de F1a debe documentar este límite y la monitorización (RF-6.3) pasa de
"conveniente" a "crítica".

### H4. Densidad del día (V6, sustenta la elección del perpetuo)

| Métrica (2026-08-19) | Perp reconstruido | Spot nativo |
| --- | --- | --- |
| Segundos sin ningún trade | 4.355 (5,04%) | 14.041 (16,25%) |
| Trades/s media | 62,4 | 48,2 |
| Trades/s p50 / p90 / p99 | 9 / 163 / 733 | 3 / 167 / 545 |
| Máximo en un segundo | 7.606 | 9.298 |

El perpetuo tiene 3× menos segundos vacíos y una mediana 3× mayor: velas de 1-10s
visiblemente más continuas. En el día tranquilo (08-18) la ventaja es aún mayor:
8,6% vs 20,7% de segundos vacíos.

## Tamaños en disco y retención

| Dato (por día, 2026-08-19) | CSV | Comprimido | Extrapolado/año |
| --- | --- | --- | --- |
| aggTrades crudos | 137,0 MB | 25,0 MB (zip) | 50,0 GB (9,1 GB zip) |
| **Velas 1s derivadas** | 7,4 MB | 1,6 MB (gzip) | **2,7 GB (0,6 GB gzip)** |
| Trades individuales (opcional, ground truth) | 285 MB | 40,9 MB (zip) | 104 GB (14,9 GB zip) |

Las velas 1s derivadas son ~18× más pequeñas que los aggTrades crudos. La política del
README (retener 1s N≈6 meses, aggTrades solo como staging temporal, histórico viejo
re-descargable) es holgada: 6 meses de velas 1s ≈ 1,4 GB en CSV plano; en TimescaleDB
comprimido será menos. No hay motivo de coste para retener aggTrades crudos.

## Caveats del veredicto

1. **Las velas 1s no son bit-perfect** contra una hipotética kline 1s oficial de
   futures (que no existe): ~17% de segundos con volumen desplazado ±0,3 BTC de media
   y ~6,5% con OHLC desviado ~0,15 USD (máx observado 52,8 USD en un segundo de
   ráfaga). Para análisis visual de trading es ruido de frontera; para algo que exija
   exactitud contable a 1s, existe la corrección batch T+1 desde `daily/trades`.
2. A nivel **1m y superior**, tras agregar, la desviación es mínima (96,7% de minutos
   exactos al satoshi en los 7 campos; close 100%) y **corregible a exacto** si en
   F1b se sobreescriben los timeframes ≥1m con las klines oficiales de bulk.
3. `trade_count` sobreestima ~0,08% por IDs quemados de STP (H2).
4. Ventana REST de 2 días (H3): condiciona el diseño de la reconciliación de F1a.
5. Todo lo anterior se ha medido en 2 días concretos (uno tranquilo, uno activo);
   los órdenes de magnitud son estables entre ambos, pero eventos extremos (flash
   crash) pueden ensanchar las colas.

## Implicaciones para F1a

- La lógica de agregación de `internal/candle` es reutilizable tal cual (streaming,
  `Builder.Add` + `Finish`).
- Paginar por `fromId` no es negociable: 14.872 trades/día perdidos si no (0,72%).
- Reconciliación de integridad: por volumen y continuidad de `aggTradeId`; nunca por
  `trade_count`.
- Reconciliación REST dentro de 48 h o esperar al bulk del día siguiente.
- Considerar en F1b un job opcional T+1 que recalcule las velas 1s del día anterior
  desde `futures/um/daily/trades` (exactitud perfecta) — barato: un fichero de 41 MB/día.
