#!/usr/bin/env bash
# Logical backup of the Jam Contests database and avatar storage.
#
# Produces, per run, a dated directory in BACKUP_DIR containing:
#   jamcontests-<ts>.dump          pg_dump custom-format logical dump
#   jamcontests-<ts>-avatars.tar.gz avatar storage archive
#   counts.txt                     row counts of every domain table
#   manifest.txt                   SHA-256 hashes and metadata
#
# Environment:
#   DATABASE_URL        read-only backup role (required)
#   AVATAR_DIR          avatar storage directory (default ../storage/avatars)
#   BACKUP_DIR          backup destination (default /var/backups/jamcontests)
#   BACKUP_KEEP         days to retain (default 30)
#   SERVICE             optional systemd unit to stop/start around the backup,
#                       e.g. jamcontests.service; started again even on failure
#   PG_DUMP             pg_dump binary (default pg_dump)
#   PSQL                psql binary (default psql)
#
# The backup is published atomically: a temporary directory is filled and
# renamed into place only when everything succeeded.

set -euo pipefail

DATABASE_URL="${DATABASE_URL:?DATABASE_URL (read-only backup role) is required}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AVATAR_DIR="${AVATAR_DIR:-$SCRIPT_DIR/../storage/avatars}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/jamcontests}"
BACKUP_KEEP="${BACKUP_KEEP:-30}"
SERVICE="${SERVICE:-}"
PG_DUMP="${PG_DUMP:-pg_dump}"
PSQL="${PSQL:-psql}"

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
fail() { log "ERROR: $*"; exit 1; }

if ! command -v "$PG_DUMP" >/dev/null 2>&1; then
    fail "pg_dump binary '$PG_DUMP' not found; install PostgreSQL client tools"
fi
if ! command -v "$PSQL" >/dev/null 2>&1; then
    fail "psql binary '$PSQL' not found; install PostgreSQL client tools"
fi
if [[ ! -d "$AVATAR_DIR" ]]; then
    fail "avatar directory '$AVATAR_DIR' does not exist"
fi

mkdir -p "$BACKUP_DIR"
exec 9>"$BACKUP_DIR/.lock"
flock -n 9 || fail "another backup is already running"

SERVICE_STOPPED=0
stop_service() {
    if [[ -n "$SERVICE" ]] && systemctl is-active --quiet "$SERVICE"; then
        log "stopping $SERVICE for a consistent backup"
        systemctl stop "$SERVICE"
        SERVICE_STOPPED=1
    fi
}
restart_service() {
    if [[ "$SERVICE_STOPPED" == 1 ]] && systemctl is-active --quiet "$SERVICE"; then
        return 0
    fi
    if [[ "$SERVICE_STOPPED" == 1 ]]; then
        log "starting $SERVICE"
        systemctl start "$SERVICE"
    fi
}
trap restart_service EXIT

TIMESTAMP="$(date -u '+%Y%m%dT%H%M%SZ')"
TEMP_DIR="$(mktemp -d "$BACKUP_DIR/.in-progress.XXXXXX")"
log "backing up to $TEMP_DIR"

stop_service

DB_NAME="$("$PSQL" "$DATABASE_URL" -tAc 'SELECT current_database()')"
log "dumping database $DB_NAME with pg_dump"
"$PG_DUMP" --format=custom --file="$TEMP_DIR/jamcontests-$TIMESTAMP.dump" "$DATABASE_URL"
log "archiving avatars"
tar -czf "$TEMP_DIR/jamcontests-$TIMESTAMP-avatars.tar.gz" -C "$AVATAR_DIR" .

log "checking avatar references against the archive"
tar -tzf "$TEMP_DIR/jamcontests-$TIMESTAMP-avatars.tar.gz" | sed 's#^\./##' | sort -u >"$TEMP_DIR/archive-entries.txt"
MISSING=0
while IFS= read -r reference; do
    if ! grep -qx "$reference" "$TEMP_DIR/archive-entries.txt"; then
        log "WARNING: avatar '$reference' referenced by a team but missing from the archive"
        MISSING=1
    fi
done < <("$PSQL" "$DATABASE_URL" -tA -c "SELECT avatar_path FROM teams WHERE avatar_path IS NOT NULL")

log "recording row counts"
{
    for table in schema_migrations users sessions admin_audit_log jams questionnaires \
        questionnaire_questions questionnaire_options questionnaire_responses \
        questionnaire_text_answers questionnaire_selected_options questionnaire_response_history \
        teams team_members team_invites team_eligibility_overrides jam_themes \
        team_theme_selections products product_bumps nominations nomination_votes; do
        count="$("$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM \"$table\"" 2>/dev/null || echo "unavailable")"
        printf '%s:%s\n' "$table" "$count"
    done
} >"$TEMP_DIR/counts.txt"

{
    printf '# jamcontests backup manifest\n'
    printf 'created_at: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'database: %s\n' "$DB_NAME"
    printf 'avatars_dir: %s\n' "$AVATAR_DIR"
    printf 'schema_migrations_count: %s\n' "$(grep '^schema_migrations:' "$TEMP_DIR/counts.txt" | cut -d: -f2)"
    printf 'sha256: %s  %s\n' "$(sha256sum "$TEMP_DIR/jamcontests-$TIMESTAMP.dump" | cut -d' ' -f1)" "jamcontests-$TIMESTAMP.dump"
    printf 'sha256: %s  %s\n' "$(sha256sum "$TEMP_DIR/jamcontests-$TIMESTAMP-avatars.tar.gz" | cut -d' ' -f1)" "jamcontests-$TIMESTAMP-avatars.tar.gz"
} >"$TEMP_DIR/manifest.txt"

PUBLISHED="$BACKUP_DIR/jamcontests-$TIMESTAMP"
rm -f "$TEMP_DIR/archive-entries.txt"
mv "$TEMP_DIR" "$PUBLISHED"
log "published backup at $PUBLISHED"
if [[ "$MISSING" == 1 ]]; then
    log "WARNING: some teams reference avatars that are not in the archive; investigate before trusting this backup"
fi

log "pruning backups older than ${BACKUP_KEEP} days"
CUTOFF="$(date -u -d "-${BACKUP_KEEP} days" '+%Y%m%dT%H%M%S')"
for candidate in "$BACKUP_DIR"/jamcontests-*; do
    [[ -d "$candidate" ]] || continue
    stamp="${candidate##*/}"
    stamp="${stamp#jamcontests-}"
    stamp="${stamp%%Z}"
    if [[ "$stamp" < "$CUTOFF" ]]; then
        log "removing old backup $candidate"
        rm -rf "$candidate"
    fi
done

log "backup complete"
