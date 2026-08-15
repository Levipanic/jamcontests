-- Opaque public identifiers for user-facing entities.
--
-- Internal bigint ids stay for administration, foreign keys and joins. Every
-- URL and JSON surface reachable by regular users must use the 18-hex-char
-- public identifier so sequential ids cannot act as a disclosure oracle for
-- hidden jams, teams, products, or nominations.
--
-- gen_random_uuid() is core since PostgreSQL 13 and uses the operating system
-- random source; no extension is required, which keeps isolated test schemas
-- (search_path confined to the test schema) self-contained.
ALTER TABLE jams ADD COLUMN public_id text;
ALTER TABLE teams ADD COLUMN public_id text;
ALTER TABLE products ADD COLUMN public_id text;
ALTER TABLE nominations ADD COLUMN public_id text;

UPDATE jams SET public_id = substr(replace(gen_random_uuid()::text, '-', ''), 1, 18) WHERE public_id IS NULL;
UPDATE teams SET public_id = substr(replace(gen_random_uuid()::text, '-', ''), 1, 18) WHERE public_id IS NULL;
UPDATE products SET public_id = substr(replace(gen_random_uuid()::text, '-', ''), 1, 18) WHERE public_id IS NULL;
UPDATE nominations SET public_id = substr(replace(gen_random_uuid()::text, '-', ''), 1, 18) WHERE public_id IS NULL;

ALTER TABLE jams ALTER COLUMN public_id SET NOT NULL;
ALTER TABLE teams ALTER COLUMN public_id SET NOT NULL;
ALTER TABLE products ALTER COLUMN public_id SET NOT NULL;
ALTER TABLE nominations ALTER COLUMN public_id SET NOT NULL;

ALTER TABLE jams ALTER COLUMN public_id SET DEFAULT substr(replace(gen_random_uuid()::text, '-', ''), 1, 18);
ALTER TABLE teams ALTER COLUMN public_id SET DEFAULT substr(replace(gen_random_uuid()::text, '-', ''), 1, 18);
ALTER TABLE products ALTER COLUMN public_id SET DEFAULT substr(replace(gen_random_uuid()::text, '-', ''), 1, 18);
ALTER TABLE nominations ALTER COLUMN public_id SET DEFAULT substr(replace(gen_random_uuid()::text, '-', ''), 1, 18);

ALTER TABLE jams ADD CONSTRAINT jams_public_id_unique UNIQUE (public_id);
ALTER TABLE teams ADD CONSTRAINT teams_public_id_unique UNIQUE (public_id);
ALTER TABLE products ADD CONSTRAINT products_public_id_unique UNIQUE (public_id);
ALTER TABLE nominations ADD CONSTRAINT nominations_public_id_unique UNIQUE (public_id);