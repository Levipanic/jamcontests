#!/usr/bin/env bash
# Verify a Jam Contests backup by restoring it into a disposable database.
#
# The backup directory must contain the manifest written by backup.sh. The
# restore target must be a separate, disposable PostgreSQL database; the script
# refuses to restore over a database that already contains the application
# schema unless FORCE=1, and refuses to restore into the source database.
#
# Environment:
#   BACKUP_DIR            backup directory to verify (required)
#   VERIFY_DATABASE_URL   disposable target database (required)
#   SOURCE_DATABASE_URL   source database URL (optional safety check)
#   VERIFY_AVATAR_DIR     avatar extraction directory (default temp)
#   MIGRATIONS_DIR        migrations directory (default ../migrations)
#   MIGRATE_BINARY        jamcontests binary; default 'go run ./cmd/jamcontests'
#   PG_RESTORE, PSQL      client binaries (default pg_restore, psql)
#   FORCE                 set to 1 to allow clobbering a schema-bearing target

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:?BACKUP_DIR is required}"
VERIFY_DATABASE_URL="${VERIFY_DATABASE_URL:?VERIFY_DATABASE_URL (disposable target database) is required}"
SOURCE_DATABASE_URL="${SOURCE_DATABASE_URL:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-$SCRIPT_DIR/../migrations}"
MIGRATE_BINARY="${MIGRATE_BINARY:-go run ./cmd/jamcontests}"
PG_RESTORE="${PG_RESTORE:-pg_restore}"
PSQL="${PSQL:-psql}"
FORCE="${FORCE:-0}"

log() { printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >&2; }
fail() { log "ERROR: $*"; exit 1; }

if [[ ! -f "$BACKUP_DIR/manifest.txt" ]]; then
    fail "no manifest.txt in $BACKUP_DIR; run scripts/backup.sh first"
fi
if ! command -v "$PG_RESTORE" >/dev/null 2>&1; then
    fail "pg_restore binary '$PG_RESTORE' not found"
fi
if ! command -v "$PSQL" >/dev/null 2>&1; then
    fail "psql binary '$PSQL' not found"
fi

extract_dbname() {
    local url="$1" path rest
    path="${url#*://}"          # strip scheme
    path="${path#*@}"           # strip credentials
    path="${path#*:}"           # strip host
    path="${path#*/}"           # strip port, keep /dbname...
    rest="${path%%\?*}"         # strip query string
    printf '%s' "${rest%%/*}"
}

SOURCE_NAME="$(extract_dbname "$SOURCE_DATABASE_URL")"
TARGET_NAME="$(extract_dbname "$VERIFY_DATABASE_URL")"
if [[ -n "$SOURCE_DATABASE_URL" && "$VERIFY_DATABASE_URL" == "$SOURCE_DATABASE_URL" ]]; then
    fail "refusing to restore into the source database"
fi
if [[ -n "$SOURCE_NAME" && "$SOURCE_NAME" == "$TARGET_NAME" ]] && [[ "$FORCE" != 1 ]]; then
    fail "target database '$TARGET_NAME' looks like the source database; set FORCE=1 to override"
fi

log "checking checksums from the manifest"
DUMP_FILE="$(grep '^sha256:.*\.dump$' "$BACKUP_DIR/manifest.txt" | awk '{print $3}')"
AVATARS_FILE="$(grep '^sha256:.*avatars.tar.gz$' "$BACKUP_DIR/manifest.txt" | awk '{print $3}')"
while read -r expected file; do
    actual="$(sha256sum "$BACKUP_DIR/$file" | cut -d' ' -f1)"
    if [[ "$actual" != "$expected" ]]; then
        fail "checksum mismatch for $file"
    fi
    log "checksum ok: $file"
done < <(grep '^sha256:' "$BACKUP_DIR/manifest.txt" | awk '{print $2, $3}')

if [[ "$FORCE" != 1 ]] && "$PSQL" "$VERIFY_DATABASE_URL" -tAc \
    "SELECT 1 FROM pg_catalog.pg_tables WHERE tablename = 'schema_migrations'" | grep -q 1; then
    fail "target database already contains schema_migrations; set FORCE=1 to restore over it"
fi

log "restoring $DUMP_FILE into $TARGET_NAME"
"$PG_RESTORE" --clean --if-exists --no-owner --no-privileges \
    --dbname="$VERIFY_DATABASE_URL" "$BACKUP_DIR/$DUMP_FILE"

log "extracting avatars"
EXTRACT_DIR="${VERIFY_AVATAR_DIR:-$(mktemp -d)}"
mkdir -p "$EXTRACT_DIR"
tar -xzf "$BACKUP_DIR/$AVATARS_FILE" -C "$EXTRACT_DIR"

log "applying current migrations on top of the restored schema (checksum check)"
(cd "$SCRIPT_DIR/.." && MIGRATION_DATABASE_URL="$VERIFY_DATABASE_URL" $MIGRATE_BINARY migrate)

log "comparing migration and row counts with the manifest"
EXPECTED_MIGRATIONS="$(grep -m1 '^schema_migrations_count:' "$BACKUP_DIR/manifest.txt" | awk '{print $2}')"
ACTUAL_MIGRATIONS="$("$PSQL" "$VERIFY_DATABASE_URL" -tAc 'SELECT count(*) FROM schema_migrations')"
if [[ "$ACTUAL_MIGRATIONS" != "$EXPECTED_MIGRATIONS" ]]; then
    fail "migration count $ACTUAL_MIGRATIONS does not match manifest $EXPECTED_MIGRATIONS"
fi
MISMATCH=0
while IFS= read -r line; do
    table="${line%%:*}"
    expected="${line#*:}"
    actual="$("$PSQL" "$VERIFY_DATABASE_URL" -tAc "SELECT count(*) FROM \"$table\"")"
    if [[ "$actual" != "$expected" ]]; then
        log "MISMATCH: $table restored $actual rows, manifest says $expected"
        MISMATCH=1
    fi
done < <(grep -v '^schema_migrations:' "$BACKUP_DIR/counts.txt")

log "checking avatar references in the restored database"
while IFS= read -r reference; do
    if [[ ! -f "$EXTRACT_DIR/$reference" ]]; then
        log "MISMATCH: restored avatar file missing: $reference"
        MISMATCH=1
    fi
done < <("$PSQL" "$VERIFY_DATABASE_URL" -tA -c "SELECT avatar_path FROM teams WHERE avatar_path IS NOT NULL")

if [[ "$MISMATCH" == 1 ]]; then
    fail "verification found mismatches; see messages above"
fi
log "verification complete: $BACKUP_DIR restored cleanly into $TARGET_NAME"
