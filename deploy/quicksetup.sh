#!/usr/bin/env bash
# One-shot production setup for a fresh Debian/Ubuntu VPS.
#
# Installs PostgreSQL and Go 1.24+, builds the Jam Contests binary, runs the
# service as the www-data system user, creates the three PostgreSQL roles from
# deploy/sql/production_roles.sql (with random passwords), writes
# /etc/jamcontests/jamcontests.env (root, 0600), applies migrations, creates
# the first administrator interactively, installs the systemd units (app plus
# the backup timer) and optionally Caddy with a domain.
#
# Run as root from the repository root:
#   sudo ./deploy/quicksetup.sh            first install
#   sudo ./deploy/quicksetup.sh --force    full re-provision (keeps avatars)
#
# Re-running without --force fails when an installation already exists so that
# live environment files are never clobbered accidentally. Avatar storage in
# /opt/jamcontests/storage/avatars survives --force.
#
# Environment:
#   GO_VERSION   Go release to install when the system Go is older than
#                go1.24 (default: latest go1.24.x from go.dev)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
fail() { log "ERROR: $*"; exit 1; }

FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

[[ "$(id -u)" -eq 0 ]] || fail "запустите от root: sudo ./deploy/quicksetup.sh"

OS_ID="$(. /etc/os-release && printf '%s' "$ID")"
if [[ "$OS_ID" != "debian" && "$OS_ID" != "ubuntu" && "$OS_ID" != "raspbian" ]]; then
    fail "поддерживаются только Debian/Ubuntu (обнаружено: $OS_ID)"
fi

for required in "$REPO_ROOT/cmd/jamcontests/main.go" \
    "$REPO_ROOT/migrations" \
    "$REPO_ROOT/templates" \
    "$REPO_ROOT/static" \
    "$SCRIPT_DIR/sql/production_roles.sql" \
    "$SCRIPT_DIR/systemd/jamcontests.service" \
    "$SCRIPT_DIR/systemd/jamcontests-backup.service" \
    "$SCRIPT_DIR/systemd/jamcontests-backup.timer" \
    "$SCRIPT_DIR/../scripts/backup.sh" \
    "$SCRIPT_DIR/../scripts/verify-backup.sh"; do
    [[ -e "$required" ]] || fail "не найден $required; запускайте из корня репозитория"
done

if [[ -d /opt/jamcontests/bin ]] || [[ -f /etc/jamcontests/jamcontests.env ]]; then
    if [[ "$FORCE" != 1 ]]; then
        fail "установка уже существует; для полной переустановки запустите с флагом --force"
    fi
    if [[ -t 0 ]]; then
        read -r -p "Полная переустановка удалит /opt/jamcontests/bin, шаблоны и статику (аватары сохранятся). Продолжить? [y/N] " answer
        [[ "${answer,,}" =~ ^y ]] || fail "отменено"
    fi
fi

if [[ -f /etc/jamcontests/jamcontests.env ]]; then
    cp /etc/jamcontests/jamcontests.env "/etc/jamcontests/jamcontests.env.bak-$(date -u '+%Y%m%dT%H%M%SZ')"
    log "старый env-файл сохранён рядом с расширением .bak-*"
fi
if [[ -f /etc/jamcontests/backup.env ]]; then
    cp /etc/jamcontests/backup.env "/etc/jamcontests/backup.env.bak-$(date -u '+%Y%m%dT%H%M%SZ')"
fi

log "шаг 1/9: системные пакеты (PostgreSQL, Go, инструменты)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates gpg openssl tar postgresql postgresql-client

systemctl enable --now postgresql >/dev/null 2>&1
for _ in $(seq 1 30); do
    pg_isready -q && break
    sleep 1
done
pg_isready -q || fail "PostgreSQL не поднялся"

log "шаг 2/9: Go 1.24+"
install_go() {
    local go_arch latest
    case "$(dpkg --print-architecture)" in
        amd64) go_arch=amd64 ;;
        arm64) go_arch=arm64 ;;
        *) fail "неподдерживаемая архитектура: $(dpkg --print-architecture)" ;;
    esac
    if [[ -z "${GO_VERSION:-}" ]]; then
        latest="$(curl -fsSL 'https://go.dev/dl/?mode=json' 2>/dev/null | grep -o '"version": "go1\.24\.[0-9]*"' | head -1 | cut -d'"' -f4 || true)"
        GO_VERSION="${latest:-go1.24.5}"
    fi
    log "установка $GO_VERSION (linux-$go_arch)"
    curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-${go_arch}.tar.gz" -o /tmp/jamcontests-go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/jamcontests-go.tar.gz
    rm -f /tmp/jamcontests-go.tar.gz
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
}
if command -v go >/dev/null 2>&1; then
    minor="$(go version | sed -n 's/.*go1\.\([0-9][0-9]*\).*/\1/p')"
    if [[ -n "$minor" && "$minor" -ge 24 ]]; then
        log "используется системный Go: $(go version | sed 's/.*\(go[0-9.]*\).*/\1/')"
    else
        install_go
    fi
else
    install_go
fi

log "шаг 3/9: системный пользователь www-data и раскладка каталогов"
if ! id -u www-data >/dev/null 2>&1; then
    useradd --system --shell /usr/sbin/nologin www-data
fi
if [[ "$FORCE" == 1 ]]; then
    rm -rf /opt/jamcontests/bin /opt/jamcontests/migrations /opt/jamcontests/templates /opt/jamcontests/static /opt/jamcontests/scripts
fi
mkdir -p /opt/jamcontests/{bin,migrations,templates,static,storage/avatars,scripts}
install -d -o www-data -g www-data -m 0750 /opt/jamcontests/storage/avatars

log "шаг 4/9: сборка бинарника"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$BUILD_DIR/jamcontests" ./cmd/jamcontests

log "шаг 5/9: база данных и роли"
if ! runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='jamcontests'" | grep -q 1; then
    runuser -u postgres -- createdb jamcontests
fi
if [[ "$(runuser -u postgres -- psql -tAc "SELECT count(*) FROM pg_roles WHERE rolname IN ('jamcontests_migrator','jamcontests_runtime','jamcontests_backup')")" != "3" ]]; then
    runuser -u postgres -- psql -v ON_ERROR_STOP=1 -f - < "$SCRIPT_DIR/sql/production_roles.sql"
fi
RUNTIME_PASS="$(openssl rand -hex 24)"
MIGRATOR_PASS="$(openssl rand -hex 24)"
BACKUP_PASS="$(openssl rand -hex 24)"
runuser -u postgres -- psql -v ON_ERROR_STOP=1 \
    -c "ALTER ROLE jamcontests_migrator PASSWORD '$MIGRATOR_PASS'" \
    -c "ALTER ROLE jamcontests_runtime PASSWORD '$RUNTIME_PASS'" \
    -c "ALTER ROLE jamcontests_backup PASSWORD '$BACKUP_PASS'"

log "шаг 6/9: установка файлов и окружения"
install -m 0750 -o root -g www-data "$BUILD_DIR/jamcontests" /opt/jamcontests/bin/jamcontests
cp -a "$REPO_ROOT/migrations/." /opt/jamcontests/migrations/
cp -a "$REPO_ROOT/templates/." /opt/jamcontests/templates/
cp -a "$REPO_ROOT/static/." /opt/jamcontests/static/
install -m 0750 -o root -g www-data \
    "$REPO_ROOT/scripts/backup.sh" "$REPO_ROOT/scripts/verify-backup.sh" /opt/jamcontests/scripts/
chown -R root:www-data /opt/jamcontests/bin /opt/jamcontests/migrations /opt/jamcontests/templates /opt/jamcontests/static
chmod -R 0750 /opt/jamcontests/bin /opt/jamcontests/migrations /opt/jamcontests/templates /opt/jamcontests/static

install -d -o root -g root -m 0700 /etc/jamcontests
umask 077
cat >/etc/jamcontests/jamcontests.env <<EOF
APP_ENV=production
HTTP_ADDR=127.0.0.1:8080
LOG_LEVEL=info
DATABASE_URL=postgres://jamcontests_runtime:${RUNTIME_PASS}@127.0.0.1:5432/jamcontests?sslmode=prefer
CSRF_SECRET=$(openssl rand -base64 48)
SESSION_COOKIE=jamcontests_session
SESSION_TTL=720h
MIGRATIONS_DIR=/opt/jamcontests/migrations
TEMPLATES_DIR=/opt/jamcontests/templates
STATIC_DIR=/opt/jamcontests/static
AVATAR_DIR=/opt/jamcontests/storage/avatars
MAX_AVATAR_BYTES=2097152
EOF
cat >/etc/jamcontests/backup.env <<EOF
DATABASE_URL=postgres://jamcontests_backup:${BACKUP_PASS}@127.0.0.1:5432/jamcontests?sslmode=prefer
AVATAR_DIR=/opt/jamcontests/storage/avatars
BACKUP_DIR=/var/backups/jamcontests
BACKUP_KEEP=30
SERVICE=jamcontests.service
EOF
chown root:root /etc/jamcontests/jamcontests.env /etc/jamcontests/backup.env
chmod 0600 /etc/jamcontests/jamcontests.env /etc/jamcontests/backup.env
umask 022

log "шаг 7/9: миграции (роль-владелец схемы)"
(cd /opt/jamcontests && MIGRATION_DATABASE_URL="postgres://jamcontests_migrator:${MIGRATOR_PASS}@127.0.0.1:5432/jamcontests?sslmode=prefer" ./bin/jamcontests migrate)
unset MIGRATOR_PASS

log "шаг 8/9: первый администратор"
if [[ -t 0 ]]; then
    read -r -p "Имя администратора (по умолчанию admin): " ADMIN_USERNAME
    ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
    read -r -p "Email администратора (необязательно): " ADMIN_EMAIL
    read -rs -p "Пароль администратора: " ADMIN_PASSWORD
    printf '\n'
    read -rs -p "Повторите пароль: " ADMIN_PASSWORD_CONFIRM
    printf '\n'
    if [[ -z "$ADMIN_PASSWORD" || "$ADMIN_PASSWORD" != "$ADMIN_PASSWORD_CONFIRM" ]]; then
        fail "пароли не совпадают или пусты"
    fi
    (cd /opt/jamcontests && ADMIN_PASSWORD="$ADMIN_PASSWORD" ./bin/jamcontests create-admin \
        --username "$ADMIN_USERNAME" ${ADMIN_EMAIL:+--email "$ADMIN_EMAIL"})
else
    log "неинтерактивный запуск: создание администратора пропущено; выполните"
    log "  (cd /opt/jamcontests && ADMIN_PASSWORD=... ./bin/jamcontests create-admin --username admin)"
fi

log "шаг 9/9: systemd-юниты"
install -m 0644 "$SCRIPT_DIR/systemd/jamcontests.service" /etc/systemd/system/
install -m 0644 "$SCRIPT_DIR/systemd/jamcontests-backup.service" /etc/systemd/system/
install -m 0644 "$SCRIPT_DIR/systemd/jamcontests-backup.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now jamcontests.service
systemctl enable --now jamcontests-backup.timer

if [[ -t 0 ]]; then
    read -r -p "Установить Caddy с TLS-доменом? [y/N] " caddy_answer
    if [[ "${caddy_answer,,}" =~ ^y ]]; then
        if ! curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key -o /tmp/caddy-key.gpg || \
            ! gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg /tmp/caddy-key.gpg; then
            log "WARNING: не удалось добавить репозиторий Caddy; настройте реверс-прокси вручную"
        else
            printf 'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main\n' \
                >/etc/apt/sources.list.d/caddy-stable.list
            apt-get update -qq && apt-get install -y -qq caddy
            read -r -p "Домен (например jam.example.org): " CADDY_DOMAIN
            if [[ -n "$CADDY_DOMAIN" ]]; then
                printf '%s {\n    reverse_proxy 127.0.0.1:8080\n}\n' "$CADDY_DOMAIN" >/etc/caddy/Caddyfile
                if ! grep -q '^TRUSTED_PROXIES=' /etc/jamcontests/jamcontests.env; then
                    printf 'TRUSTED_PROXIES=127.0.0.1\n' >>/etc/jamcontests/jamcontests.env
                fi
                systemctl enable --now caddy
                systemctl restart jamcontests.service
                log "Caddy настроен на https://$CADDY_DOMAIN (рестарт jamcontests для TRUSTED_PROXIES)"
            fi
            rm -f /tmp/caddy-key.gpg
        fi
    fi
fi

log "ожидание готовности /health"
READY=0
for _ in $(seq 1 30); do
    if curl -fsS http://127.0.0.1:8080/health >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 2
done
[[ "$READY" == 1 ]] || fail "сервис не ответил на /health в течение 60 секунд; смотрите: journalctl -u jamcontests.service -e"

log "установка завершена:"
log "  приложение:  systemctl status jamcontests.service"
log "  бэкапы:      systemctl list-timers jamcontests-backup.timer (каждые 6 часов)"
log "  проверка:    curl http://127.0.0.1:8080/health"
log "  быстрый старт: sudo ./deploy/start.sh (или systemctl restart jamcontests.service)"
log "  env-файл:    /etc/jamcontests/jamcontests.env (root, 0600)"
log "  пароли ролей сгенерированы случайно; прежние версии env-файлов сохранены с суффиксом .bak-*"