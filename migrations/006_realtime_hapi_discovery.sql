\set ON_ERROR_STOP on

BEGIN;

INSERT INTO app_settings (key, value)
VALUES ('api_contract_version', '15'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

INSERT INTO schema_migrations(version, description)
VALUES (6, 'realtime HAPI connection discovery')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
