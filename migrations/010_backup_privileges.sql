-- Read-only privileges for the backup role.
--
-- The backup role runs pg_dump and verification queries and must never write.
-- Grants are conditional, like the runtime grants, so development and isolated
-- test schemas are unaffected.
DO $$
DECLARE
    backup_role text := 'jamcontests_backup';
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = backup_role) THEN
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', current_schema(), backup_role);
        EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO %I', current_schema(), backup_role);
        EXECUTE format('GRANT SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', current_schema(), backup_role);
    END IF;
END;
$$;
