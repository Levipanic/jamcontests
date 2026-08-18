#!/usr/bin/env bash
# Quick start/stop/status of the production Jam Contests service.
#
# Installed by deploy/quicksetup.sh:
#   sudo ./deploy/start.sh           start or restart the whole application
#   sudo ./deploy/start.sh --stop    stop the application and the backup timer
#   sudo ./deploy/start.sh --status  show unit status without starting anything
#
# The start mode waits until GET /health answers 200 (the app serves 503 while
# PostgreSQL or the schema is unavailable).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
fail() { log "ERROR: $*"; exit 1; }

MODE=start
case "${1:-}" in
    --status) MODE=status ;;
    --stop) MODE=stop ;;
esac

[[ "$(id -u)" -eq 0 ]] || fail "запустите от root: sudo ./deploy/start.sh"

if [[ ! -f /etc/systemd/system/jamcontests.service ]]; then
    fail "юнит jamcontests.service не установлен; сначала выполните sudo ./deploy/quicksetup.sh"
fi

if [[ "$MODE" == "status" ]]; then
    systemctl status --no-pager jamcontests.service jamcontests-backup.timer || true
    exit 0
fi

if [[ "$MODE" == "stop" ]]; then
    log "остановка приложения и таймера бэкапов"
    systemctl stop jamcontests.service
    systemctl stop jamcontests-backup.timer 2>/dev/null || log "WARNING: таймер бэкапов не удалось остановить"
    log "остановлено: app=$(systemctl is-active jamcontests.service), timer=$(systemctl is-active jamcontests-backup.timer)"
    exit 0
fi

log "запуск приложения и таймера бэкапов"
systemctl start jamcontests.service
systemctl start jamcontests-backup.timer 2>/dev/null || log "WARNING: таймер бэкапов не удалось запустить"

HEALTH_URL="http://127.0.0.1:8080/health"
if [[ -f /etc/jamcontests/jamcontests.env ]]; then
    HTTP_ADDR="$(grep -E '^HTTP_ADDR=' /etc/jamcontests/jamcontests.env | cut -d= -f2- || true)"
    if [[ -n "$HTTP_ADDR" && "$HTTP_ADDR" != :* ]]; then
        HEALTH_URL="http://${HTTP_ADDR}/health"
    fi
fi

log "ожидание готовности: $HEALTH_URL"
READY=0
for _ in $(seq 1 30); do
    if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 2
done
if [[ "$READY" != 1 ]]; then
    fail "сервис не ответил на /health в течение 60 секунд; смотрите: journalctl -u jamcontests.service -e"
fi

log "приложение готово: $(systemctl is-active jamcontests.service)"
log "логи:            journalctl -u jamcontests.service -f"
log "адрес:           http://127.0.0.1:8080 (за реверс-прокси, см. /etc/caddy/Caddyfile)"
log "проверка:        curl $HEALTH_URL"