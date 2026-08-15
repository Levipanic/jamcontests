-- PostgreSQL cluster roles for Jam Contests.
--
-- Run this as the cluster superuser (or a role with CREATEROLE and ownership
-- of the jamcontests database) once per database cluster. Roles are cluster
-- objects, so the script is intentionally not a schema migration. Three roles:
--
--   jamcontests_migrator  owns the schema, runs migrations, never serves traffic
--   jamcontests_runtime   runs the application with least privilege
--   jamcontests_backup    read-only role for backups and verification
--
-- Passwords are environment concerns; set them with ALTER ROLE after creation.
-- Migration 009_runtime_privileges grants the runtime role table-level DML when
-- it exists, so create roles before running the migrations for the first time.

CREATE ROLE jamcontests_migrator LOGIN;
CREATE ROLE jamcontests_runtime LOGIN;
CREATE ROLE jamcontests_backup LOGIN;

GRANT CONNECT ON DATABASE jamcontests TO jamcontests_runtime, jamcontests_backup;

-- The migrator role owns the schema and all objects in it; the runtime role
-- receives DML grants from the migrations. Default privileges are intentionally
-- not used: every future migration must grant runtime DML explicitly so that a
-- new audit-like table can never silently gain UPDATE or DELETE for the app.
GRANT ALL PRIVILEGES ON DATABASE jamcontests TO jamcontests_migrator;
GRANT CREATE ON SCHEMA public TO jamcontests_migrator;
GRANT USAGE ON SCHEMA public TO jamcontests_runtime, jamcontests_backup;
