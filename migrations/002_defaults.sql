\set ON_ERROR_STOP on

BEGIN;

INSERT INTO app_settings(key, value) VALUES
    ('product', '"EchoEar Cloud"'::jsonb),
    ('api_contract_version', '12'::jsonb),
    ('default_locale', '"zh-CN"'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- Preserve an existing installation's first account as its administrator.
-- This runs before optional bootstrap creation so an established account is
-- never displaced by a second, automatically-created administrator.
WITH first_user AS (
    SELECT id FROM users ORDER BY id LIMIT 1
)
UPDATE users
SET role = 'admin', updated_at = NOW()
WHERE id = (SELECT id FROM first_user)
  AND NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin');

INSERT INTO users(username, password_hash, email, role, password_changed)
SELECT
    :'bootstrap_admin_username',
    crypt(:'bootstrap_admin_password', gen_salt('bf', 12)),
    :'bootstrap_admin_email',
    'admin',
    FALSE
WHERE :'bootstrap_admin_username' <> '' AND :'bootstrap_admin_password' <> ''
  AND NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin')
ON CONFLICT DO NOTHING;

INSERT INTO user_settings(user_id)
SELECT id FROM users
ON CONFLICT (user_id) DO NOTHING;

INSERT INTO schema_migrations(version, description)
VALUES (2, 'application defaults and optional bootstrap administrator')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
