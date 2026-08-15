-- Production runtime privileges.
--
-- The migration owner role owns the schema and runs the migrations. The
-- application runtime role must be provisioned by the DBA (see
-- deploy/sql/production_roles.sql) and must receive only the DML it needs.
-- Grants are applied only when the role exists, so development and isolated
-- test schemas are unaffected. Future migrations that create domain tables
-- must grant runtime DML explicitly; the audit log must stay append-only for
-- the runtime role (only SELECT and INSERT).
DO $$
DECLARE
    runtime_role text := 'jamcontests_runtime';
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = runtime_role) THEN
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', current_schema(), runtime_role);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', current_schema(), runtime_role);
        EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', current_schema(), runtime_role);
        EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON admin_audit_log FROM %I', runtime_role);
    END IF;
END;
$$;
