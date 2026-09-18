\set ON_ERROR_STOP on
-- Execute only against an empty disposable database.
CREATE TABLE groups (
    id bigint PRIMARY KEY,
    models_list_config jsonb NOT NULL DEFAULT '{}'
);
INSERT INTO groups VALUES (1, '{"enabled":true,"models":["before"]}');
\ir ../mao-model-allowlist-compat.sql
\ir ../../backend/migrations/235_group_model_allowlist.sql
\ir ../../backend/migrations/236_group_model_allowlist_repair.sql

UPDATE groups SET models_list_config = '{"models":["blue"]}' WHERE id = 1;
DO $$ BEGIN
    IF (SELECT model_allowlist FROM groups WHERE id = 1) <> '{"models":["blue"]}'::jsonb THEN
        RAISE EXCEPTION 'Blue update lost';
    END IF;
END $$;
UPDATE groups SET model_allowlist = '{"models":["green"]}' WHERE id = 1;
DO $$ BEGIN
    IF (SELECT models_list_config FROM groups WHERE id = 1) <> '{"models":["green"]}'::jsonb THEN
        RAISE EXCEPTION 'Green update lost';
    END IF;
END $$;
INSERT INTO groups(id, models_list_config) VALUES (2, '{"models":["blue-insert"]}');
INSERT INTO groups(id, model_allowlist) VALUES (3, '{"models":["green-insert"]}');
\ir ../mao-model-allowlist-compat.sql
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM groups WHERE models_list_config IS DISTINCT FROM model_allowlist) THEN
        RAISE EXCEPTION 'Columns diverged';
    END IF;
    BEGIN
        UPDATE groups SET models_list_config = '{"models":["a"]}', model_allowlist = '{"models":["b"]}' WHERE id = 1;
        RAISE EXCEPTION 'Conflicting update was accepted';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM <> 'Conflicting model allowlist updates' THEN RAISE; END IF;
    END;
END $$;
UPDATE groups SET model_allowlist = '{}' WHERE id = 1;
DO $$ BEGIN
    IF (SELECT models_list_config FROM groups WHERE id = 1) <> '{}'::jsonb THEN
        RAISE EXCEPTION 'Clear failed';
    END IF;
END $$;
SELECT 'PASS: migration, blue/green writes, inserts, clear, conflict rejection, idempotence' AS result;
