# F0 — Spike: velas de 1s desde aggTrades

Valida la hipótesis central del proyecto: reconstruir velas de 1s del perpetuo
BTCUSDT (futures UM) desde aggTrades. Resultados y veredicto en
[RESULTADOS.md](RESULTADOS.md).

## Uso

Los datos crudos no van al repo (`.gitignore`). Descarga y verificación:

```bash
cd data/raw
for url in \
  "https://data.binance.vision/data/futures/um/daily/aggTrades/BTCUSDT/BTCUSDT-aggTrades-2026-08-19.zip" \
  "https://data.binance.vision/data/futures/um/daily/klines/BTCUSDT/1m/BTCUSDT-1m-2026-08-19.zip" \
  "https://data.binance.vision/data/spot/daily/klines/BTCUSDT/1s/BTCUSDT-1s-2026-08-19.zip" \
  "https://data.binance.vision/data/futures/um/daily/trades/BTCUSDT/BTCUSDT-trades-2026-08-19.zip"; do
  curl -sO "$url" && curl -sO "$url.CHECKSUM"
done
sha256sum -c *.CHECKSUM && for z in *.zip; do unzip -o -q "$z"; done
```

Comandos (la fecha es la constante `date` en `main.go`):

```bash
go run . validate      # V1-V4, V6 + escribe data/candles_1s_perp.csv
go run . simulate      # pérdida por paginación startTime, simulada sobre bulk
go run . rest -strategy=fromid     # descarga REST del día (correcta)
go run . rest -strategy=starttime  # descarga REST del día (incorrecta, a propósito)
go run . compare-rest  # V5 + pérdida real de la estrategia startTime
go run . diag          # caracterización de discrepancias V1
go run . truth         # ground truth desde trades individuales
```

## Estructura

- `internal/candle` — agregación aggTrades→1s (**reutilizable en F1a**)
- `internal/binance` — lectores de CSV bulk + cliente REST con throttling
- `internal/fixed` — decimales en punto fijo int64 (escala 1e8), comparación exacta
