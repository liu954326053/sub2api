-- Run before starting v0.2.2+ alongside an older Mao instance.
-- Keep both column names writable for blue-green coexistence and rollback.
-- Do not remove the legacy column until the rollback window has closed.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
LOCK TABLE groups IN ACCESS EXCLUSIVE MODE;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS models_list_config jsonb NOT NULL DEFAULT '{}';
ALTER TABLE groups ADD COLUMN IF NOT EXISTS model_allowlist jsonb NOT NULL DEFAULT '{}';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM groups
        WHERE models_list_config <> '{}'::jsonb AND model_allowlist <> '{}'::jsonb
          AND models_list_config IS DISTINCT FROM model_allowlist
    ) THEN
        RAISE EXCEPTION 'Conflicting model allowlist columns; reconcile before deployment';
    END IF;
END $$;

UPDATE groups SET model_allowlist = models_list_config
WHERE model_allowlist = '{}'::jsonb AND models_list_config <> '{}'::jsonb;
UPDATE groups SET models_list_config = model_allowlist
WHERE models_list_config IS DISTINCT FROM model_allowlist;

CREATE OR REPLACE FUNCTION mao_sync_group_model_allowlist() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.models_list_config <> '{}'::jsonb AND NEW.model_allowlist <> '{}'::jsonb
           AND NEW.models_list_config IS DISTINCT FROM NEW.model_allowlist THEN
            RAISE EXCEPTION 'Conflicting model allowlist values';
        END IF;
        IF NEW.model_allowlist = '{}'::jsonb THEN
            NEW.model_allowlist := NEW.models_list_config;
        ELSE
            NEW.models_list_config := NEW.model_allowlist;
        END IF;
    ELSIF NEW.models_list_config IS DISTINCT FROM OLD.models_list_config THEN
        IF NEW.model_allowlist IS DISTINCT FROM OLD.model_allowlist
           AND NEW.models_list_config IS DISTINCT FROM NEW.model_allowlist THEN
            RAISE EXCEPTION 'Conflicting model allowlist updates';
        END IF;
        NEW.model_allowlist := NEW.models_list_config;
    ELSE
        NEW.models_list_config := NEW.model_allowlist;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS mao_group_model_allowlist_compat ON groups;
CREATE TRIGGER mao_group_model_allowlist_compat
BEFORE INSERT OR UPDATE ON groups
FOR EACH ROW EXECUTE FUNCTION mao_sync_group_model_allowlist();
COMMIT;
