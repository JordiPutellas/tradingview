#!/usr/bin/env bash
# Suites de frontend contra la base de datos de TEST.  Uso: ./run-tests.sh [app|draw]
#
# Hasta F4 esto corría contra la base de datos de PRODUCCIÓN por el túnel y las
# suites, que vacían la tabla de dibujos para empezar limpias, se llevaron por
# delante los del usuario. Ahora hay una instancia aparte en el servidor
# (perfil "test" del compose, puerto 5434) con datos sintéticos.
#
# Este script levanta el camino entero y lo desmonta al salir:
#   túnel 15434 -> jordios:5434  ·  API local en 8090 contra btc_test  ·  suites
#
# La API publica el nombre de la base de datos en /api/health y las suites
# ABORTAN si no acaba en _test: aunque este script se salte, no hay forma de
# que un test escriba en producción.
set -euo pipefail
cd "$(dirname "$0")"

HOST=${HOST:-jordios}
DEST=${DEST:-btcdash/collector}
PUERTO_TUNEL=${PUERTO_TUNEL:-15434}
API_PUERTO=${API_PUERTO:-8090}
DB_URL="postgres://btc:${POSTGRES_PASSWORD:-btc}@127.0.0.1:${PUERTO_TUNEL}/btc_test"

case "${1:-todo}" in
  app)  SUITES=(app-test.mjs) ;;
  draw) SUITES=(draw-test.mjs) ;;
  todo) SUITES=(app-test.mjs draw-test.mjs) ;;
  *) echo "uso: $0 [app|draw]" >&2; exit 2 ;;
esac

tunel_pid=""; api_pid=""
limpiar() {
  [ -n "$api_pid" ] && kill "$api_pid" 2>/dev/null || true
  [ -n "$tunel_pid" ] && kill "$tunel_pid" 2>/dev/null || true
}
trap limpiar EXIT

echo "· arrancando la base de datos de test en $HOST"
ssh "$HOST" "cd $DEST && docker compose --profile test up -d timescaledb-test" >/dev/null

# Sembrar SIEMPRE (salvo SKIP_SEED=1). El feeder se para al terminar cada
# ejecución, así que entre una y otra la semilla envejece: quedaría un agujero
# entre la última vela sembrada y la primera del feeder nuevo, y la mitad de
# las comprobaciones (carga inicial, streaming, ventanas dias(n)) se irían al
# garete por un motivo que no tiene nada que ver con lo que prueban.
if [ "${SKIP_SEED:-0}" != 1 ]; then
  echo "· sembrando datos sintéticos (SKIP_SEED=1 para saltarlo)"
  # OJO: la salida NO puede ir a /dev/null. `docker compose run` con stdout a
  # /dev/null mata el contenedor a los tres segundos —justo después de que la
  # semilla borre el esquema—, y el guion seguía como si nada con la base de
  # datos vacía. A un fichero funciona, y además queda el log.
  if ! ssh "$HOST" "cd $DEST && docker compose --profile test run --rm -T seed-test -days ${SEED_DAYS:-400} -hours-1s ${SEED_HOURS_1S:-6}" > /tmp/seed-test.log 2>&1; then
    echo "ABORTADO: la siembra falló" >&2; tail -5 /tmp/seed-test.log >&2; exit 1
  fi
  echo "  $(grep -c . /tmp/seed-test.log) líneas de siembra, última: $(tail -1 /tmp/seed-test.log)"
fi
ssh "$HOST" "cd $DEST && docker compose --profile test up -d feeder-test" >/dev/null

echo "· túnel 127.0.0.1:$PUERTO_TUNEL -> $HOST:5434"
ssh -N -L "$PUERTO_TUNEL:127.0.0.1:5434" "$HOST" & tunel_pid=$!
for _ in $(seq 1 20); do
  (echo > /dev/tcp/127.0.0.1/$PUERTO_TUNEL) 2>/dev/null && break
  sleep 0.5
done

echo "· compilando el frontend"
(cd web && npm run build >/dev/null)

echo "· API local en 127.0.0.1:$API_PUERTO contra btc_test"
(cd collector && DATABASE_URL="$DB_URL" STATIC_DIR=../web/dist API_ADDR="127.0.0.1:$API_PUERTO" \
  go run ./cmd/api > /tmp/api-test.log 2>&1) & api_pid=$!
for _ in $(seq 1 60); do
  salud=$(curl -s -m 2 "http://127.0.0.1:$API_PUERTO/api/health" || true)
  case "$salud" in *'"db":"btc_test"'*) break ;; esac
  sleep 1
done
case "${salud:-}" in
  *'"db":"btc_test"'*) echo "· API lista: $salud" ;;
  *) echo "ABORTADO: la API no responde con la base de datos de test ($salud)" >&2
     tail -5 /tmp/api-test.log >&2; exit 1 ;;
esac

fallos=0
for s in "${SUITES[@]}"; do
  echo "· $s"
  (cd web && node "$s") || fallos=1
done

echo "· parando la base de datos de test"
ssh "$HOST" "cd $DEST && docker compose --profile test stop timescaledb-test feeder-test" >/dev/null
exit $fallos
