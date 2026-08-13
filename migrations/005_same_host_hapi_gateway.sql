\set ON_ERROR_STOP on

BEGIN;

INSERT INTO app_settings (key, value)
VALUES ('api_contract_version', '14'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

INSERT INTO schema_migrations(version, description)
VALUES (5, 'same-host HAPI HTTP and SSE gateway')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
