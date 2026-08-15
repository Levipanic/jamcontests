# Backups and restore verification

Jam Contests data lives in PostgreSQL and in the avatar storage directory.
Both must be backed up together: a database dump without the avatars is
incomplete, and an avatar archive without the dump cannot be restored
consistently.

## What a backup contains

`scripts/backup.sh` publishes a dated directory in `BACKUP_DIR` (default
`/var/backups/jamcontests`):

```text
jamcontests-20260715T061500Z/
  jamcontests-20260715T061500Z.dump            pg_dump custom-format logical dump
  jamcontests-20260715T061500Z-avatars.tar.gz  avatar storage archive
  counts.txt                                   row count of every domain table
  manifest.txt                                 SHA-256 hashes and metadata
```

The dump is a single consistent snapshot of the database. When the systemd
`SERVICE` unit is configured, the application is stopped gracefully for the
duration of the dump and archive creation and started again on exit, including
after a failure. The script also checks that every `teams.avatar_path` is
present in the avatar archive and prunes backups older than `BACKUP_KEEP` days
(default 30). Publishing is atomic: a temporary directory is renamed into place
only after the manifest is written.

## Setup

1. Copy `deploy/systemd/jamcontests-backup.{service,timer}` to
   `/etc/systemd/system/`.
2. Create the environment file (root-owned, mode 0600):

   ```bash
   install -m 0600 /dev/null /etc/jamcontests/backup.env
   # edit:
   #   DATABASE_URL=postgres://jamcontests_backup:...@127.0.0.1:5432/jamcontests?sslmode=verify-full
   #   AVATAR_DIR=/opt/jamcontests/storage/avatars
   #   BACKUP_DIR=/var/backups/jamcontests
   #   BACKUP_KEEP=30
   #   SERVICE=jamcontests.service
   ```

   The backup role needs only `SELECT` on the domain tables (granted by
   migration `010_backup_privileges.sql` when the role exists); it must never
   be allowed to write.
3. Install the scripts under `/opt/jamcontests/scripts/` and make them
   executable, then enable the timer:

   ```bash
   systemctl daemon-reload
   systemctl enable --now jamcontests-backup.timer
   systemctl list-timers jamcontests-backup.timer
   ```

## Offsite copy

Keep at least one offsite copy of the backup directory, for example with
`rclone` or `restic`:

```bash
rclone copy /var/backups/jamcontests backup-remote:jamcontests --transfers 4
```

Encrypt the offsite copy at rest. The backup directory should live on the same
VPS; the offsite copy protects against host loss.

## Verification

Backups are only as good as their restore. Run the verification on a
disposable PostgreSQL instance (fresh container, scratch cluster):

```bash
export SOURCE_DATABASE_URL='postgres://jamcontests_backup:...@127.0.0.1:5432/jamcontests?sslmode=verify-full'
export BACKUP_DIR='/var/backups/jamcontests/jamcontests-20260715T061500Z'
export VERIFY_DATABASE_URL='postgres://verify:...@127.0.0.1:5432/jamcontests_verify?sslmode=disable'
scripts/verify-backup.sh
```

The script refuses to restore over a database that already has the application
schema and refuses targets that look like the source database. It then:

1. verifies both SHA-256 checksums from the manifest;
2. restores the dump with `pg_restore --no-owner --no-privileges`;
3. extracts the avatar archive;
4. runs the current migrations on top of the restored schema, which fails if a
   migration checksum changed after it was applied;
5. compares `schema_migrations` and every domain-table row count with the
   manifest;
6. checks that every restored `teams.avatar_path` file exists.

Production policy: run a verified restore at least once per quarter and always
after a schema migration release. After verification, drop the disposable
database and cluster.

## Restore (disaster recovery)

1. Bring up a fresh PostgreSQL and create the database and roles
   (`deploy/sql/production_roles.sql`).
2. Restore the dump: `pg_restore --no-owner --no-privileges --clean --if-exists --dbname=$DATABASE_URL <dump>`.
3. Restore the avatar archive into `AVATAR_DIR` preserving owner and mode.
4. Run `jamcontests migrate` with the migrator role; the checksum check
   confirms the schema matches the shipped migrations.
5. Set `jamcontests_runtime` and `jamcontests_backup` role passwords and start
   the service; `/health` returns 200 only when the schema is complete.
