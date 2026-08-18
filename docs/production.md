# Production deployment

This document describes the recommended single-VPS production setup for Jam
Contests. The application is a Go binary behind an HTTPS reverse proxy with
systemd, PostgreSQL on the same host, and nightly verified backups.

## 1. Roles and privileges

The application never runs migrations against the runtime database URL and the
server process never receives migration owner credentials:

| Role                  | Purpose                                             | Credentials in              |
|-----------------------|-----------------------------------------------------|-----------------------------|
| `jamcontests_migrator`| owns the schema, runs `migrate`                     | shell session / deploy step |
| `jamcontests_runtime` | serves traffic with least privilege                 | `DATABASE_URL`              |
| `jamcontests_backup`  | read-only access for `pg_dump` and restore checks   | backup script environment   |

Create the roles with `deploy/sql/production_roles.sql`, then set passwords:

```sql
ALTER ROLE jamcontests_migrator PASSWORD '...';
ALTER ROLE jamcontests_runtime PASSWORD '...';
ALTER ROLE jamcontests_backup PASSWORD '...';
```

Migration `009_runtime_privileges.sql` grants the runtime role DML on the
domain tables and only `SELECT, INSERT` on `admin_audit_log` whenever the role
exists. The append-only guarantee is enforced twice: by PostgreSQL triggers and
by the absence of `UPDATE`, `DELETE`, `TRUNCATE` grants for the runtime role.

Every future migration that creates domain tables must explicitly grant runtime
DML; default privileges are deliberately not used, so a new audit-like table
can never silently become writable for the application.

## 2. Directory layout and systemd

```text
/opt/jamcontests/bin/jamcontests            the compiled binary (root:www-data 0750)
/opt/jamcontests/migrations/                migration files, read-only
/opt/jamcontests/templates/ static/         read-only assets
/opt/jamcontests/storage/avatars/           avatar uploads (owned by www-data, 0750)
/etc/jamcontests/jamcontests.env            environment file (root, mode 0600)
```

Deploy the unit from `deploy/systemd/jamcontests.service`:

```bash
install -d -o www-data -g www-data /opt/jamcontests/storage/avatars
cp deploy/env.production.example /etc/jamcontests/jamcontests.env
chmod 0600 /etc/jamcontests/jamcontests.env
# edit the secrets and the database URLs, then:
systemctl enable --now jamcontests
```

The service runs with `ProtectSystem=strict`; only the avatar and storage
directories are writable. Secrets never appear in the environment file's name
or in logs; `http.Server` errors go through the structured logger.

## 3. Reverse proxy and TLS

The application listens on `127.0.0.1:8080` (set `HTTP_ADDR`). Put it behind
Caddy or nginx that terminates TLS. Configure:

- pass `X-Forwarded-For` and set `TRUSTED_PROXIES=127.0.0.1` (or the proxy CIDR)
  so rate limiting sees real client IPs; keep the list exact and minimal;
- never expose the Go port publicly;
- keep the upstream request headers under 1 MiB (`MaxHeaderBytes`).

Example Caddyfile:

```caddyfile
jam.example.org {
    reverse_proxy 127.0.0.1:8080
}
```

## 4. Health and readiness

`GET /health` returns `200 {"status":"ok"}` only when PostgreSQL answers and
the schema is fully migrated (`schema_migrations` matches the migration files
checksum-for-checksum); otherwise `503 {"status":"unavailable"}`. The probe
never sets cookies and performs no session work. Point the load balancer or
monitoring at `/health` every 10 seconds.

## 5. Migrations

Migrations run once per deploy with the owner role and are idempotent:

```bash
export MIGRATION_DATABASE_URL='postgres://jamcontests_migrator:...@127.0.0.1:5432/jamcontests?sslmode=verify-full'
/opt/jamcontests/bin/jamcontests migrate
```

`MIGRATION_DATABASE_URL` is required outside development: the runtime role must
not own the schema. After migrating, restart the service; the readiness probe
fails closed if a migration is missing.

## 6. Backups and restore

See `docs/backup-restore.md` and `deploy/systemd/jamcontests-backup.{service,timer}`.
Backups run from the host every 6 hours, keep 30 days of copies, and are
verified by a restore into a disposable database.

## 7. Operational notes

- Logs are JSON in production (`LOG_LEVEL=info` default); collect them with
  journald or your collector of choice. `X-Request-ID` correlates requests.
- Admin access is only via the directly entered `/admin` route; the UI never
  links to it.
- Run `jamcontests create-admin --username admin` with `ADMIN_PASSWORD` set to
  bootstrap the first administrator before the first public launch.
- The session cookie and the `__Host-jamcontests_csrf` cookie are `HttpOnly`,
  `Secure`, `SameSite=Lax`.
