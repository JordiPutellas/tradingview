#!/usr/bin/env bash
# Deploy a jordios.  Uso:  ./deploy.sh [--web] [--no-build]
#
#   --web       compila web/ y sincroniza también el frontend (webdist/)
#   --no-build  no reconstruye la imagen (solo sincroniza ficheros)
#
# El rsync PISA los ficheros versionados del servidor. Por eso la config
# específica del servidor vive en ~/btcdash/collector/.env, que este script
# nunca toca y cuya ausencia ABORTA el deploy: en F1b la HEALTHCHECK_URL se
# había activado editando el docker-compose.yml del servidor, un deploy la
# pisó y la monitorización quedó muda sin que nadie avisara — porque lo que
# dejó de avisar era el propio avisador.
#
# Para saltarse el guardia a sabiendas: ALLOW_NO_HEALTHCHECK=1 ./deploy.sh
set -euo pipefail
cd "$(dirname "$0")"

HOST=${HOST:-jordios}
DEST=${DEST:-btcdash/collector}
WEB=0; BUILD=1
for a in "$@"; do
  case "$a" in
    --web) WEB=1 ;;
    --no-build) BUILD=0 ;;
    *) echo "opción desconocida: $a" >&2; exit 2 ;;
  esac
done

# --- guardia de configuración del servidor ---
if ! ssh "$HOST" "test -s $DEST/.env"; then
  echo "ABORTADO: $HOST:$DEST/.env no existe o está vacío." >&2
  echo "La config del servidor va SIEMPRE ahí (RUNBOOK §0). Mínimo:" >&2
  echo "  HEALTHCHECK_URL=https://hc-ping.com/<uuid>" >&2
  echo "Para desplegar igualmente: ALLOW_NO_HEALTHCHECK=1 $0 $*" >&2
  [ "${ALLOW_NO_HEALTHCHECK:-0}" = 1 ] || exit 1
elif ! ssh "$HOST" "grep -qE '^HEALTHCHECK_URL=.+' $DEST/.env"; then
  echo "ABORTADO: $DEST/.env no define HEALTHCHECK_URL: la monitorización quedaría muda." >&2
  [ "${ALLOW_NO_HEALTHCHECK:-0}" = 1 ] || exit 1
fi

# --- código ---
# --exclude 'backups/': ahí viven las copias (F5). Sin esta línea, un deploy
# con --delete se llevaría por delante justo lo que protege de un desastre.
rsync -az --delete --exclude 'webdist/' --exclude 'data/' --exclude '.env' \
  --exclude 'backups/' collector/ "$HOST:$DEST/"

# --- frontend ---
if [ "$WEB" = 1 ]; then
  (cd web && npm run build)
  rsync -az --delete web/dist/ "$HOST:$DEST/webdist/"
fi

# --- contenedores ---
if [ "$BUILD" = 1 ]; then
  # --profile test/ops en el build: esas imágenes (seed-test, backup, restore)
  # NO se reconstruyen con `up -d --build`, porque `up` no arranca lo que está
  # tras un perfil. Sin esta línea se quedan con el binario de hace tres
  # despliegues y depurar eso cuesta una tarde.
  ssh "$HOST" "cd $DEST && docker compose --profile test --profile ops build && docker compose up -d --build"
else
  ssh "$HOST" "cd $DEST && docker compose up -d"
fi

# --- verificación: que la monitorización externa siga viva tras el deploy ---
sleep 5
health=$(ssh "$HOST" 'curl -s -m 5 http://127.0.0.1:8081/health' || true)
echo "$health"
case "$health" in
  *'"healthcheck_ping":true'*) echo "OK: ping externo activo" ;;
  *) echo "AVISO: healthcheck_ping=false — la monitorización externa NO está activa" >&2 ;;
esac
ssh "$HOST" 'curl -s -m 5 http://127.0.0.1:8080/api/health' && echo
