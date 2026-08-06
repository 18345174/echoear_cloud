\set ON_ERROR_STOP on

BEGIN;

-- Reconcile role state on every container start. Every statement is
-- idempotent because the entrypoint intentionally replays all migrations.
ALTER TABLE users ADD COLUMN IF NOT EXISTS role TEXT;
UPDATE users SET role = 'user' WHERE role IS NULL OR role NOT IN ('admin', 'user');
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'user';
ALTER TABLE users ALTER COLUMN role SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'users'::regclass
          AND conname = 'users_role_check'
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'user'));
    END IF;
END
$$;

WITH first_user AS (
    SELECT id FROM users ORDER BY id LIMIT 1
)
UPDATE users
SET role = 'admin', updated_at = NOW()
WHERE id = (SELECT id FROM first_user)
  AND NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin');

INSERT INTO user_settings(user_id)
SELECT id FROM users
ON CONFLICT (user_id) DO NOTHING;

INSERT INTO app_settings(key, value)
VALUES ('api_contract_version', '12'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

INSERT INTO schema_migrations(version, description)
VALUES (3, 'account roles and administrator-only registration')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
