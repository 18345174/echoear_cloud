\set ON_ERROR_STOP on

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS status TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_by BIGINT NULL REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_by BIGINT NULL REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_reason TEXT NOT NULL DEFAULT '';
UPDATE users SET status = 'active' WHERE status IS NULL OR status NOT IN ('active', 'disabled', 'deleted');
ALTER TABLE users ALTER COLUMN status SET DEFAULT 'active';
ALTER TABLE users ALTER COLUMN status SET NOT NULL;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'users'::regclass AND conname = 'users_status_check'
    ) THEN
        ALTER TABLE users ADD CONSTRAINT users_status_check
            CHECK (status IN ('active', 'disabled', 'deleted'));
    END IF;
END
$$;
CREATE INDEX IF NOT EXISTS users_status_role_idx ON users(status, role);

ALTER TABLE agents ADD COLUMN IF NOT EXISTS public_id UUID;
UPDATE agents SET public_id = gen_random_uuid() WHERE public_id IS NULL;
ALTER TABLE agents ALTER COLUMN public_id SET DEFAULT gen_random_uuid();
ALTER TABLE agents ALTER COLUMN public_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS agents_public_id_unique ON agents(public_id);

CREATE TABLE IF NOT EXISTS agent_shares (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id          BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    owner_user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    grantee_user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'active', 'declined', 'revoked', 'expired')),
    valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until       TIMESTAMPTZ NULL,
    policy            JSONB NOT NULL DEFAULT '{}'::jsonb,
    policy_version    INTEGER NOT NULL DEFAULT 1 CHECK (policy_version > 0),
    accepted_at       TIMESTAMPTZ NULL,
    accepted_by       BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    declined_at       TIMESTAMPTZ NULL,
    revoked_at        TIMESTAMPTZ NULL,
    revoked_by        BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    revoke_reason     TEXT NOT NULL DEFAULT '',
    created_by        BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (owner_user_id <> grantee_user_id),
    CHECK (valid_until IS NULL OR valid_until > valid_from)
);
CREATE INDEX IF NOT EXISTS agent_shares_owner_idx ON agent_shares(owner_user_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS agent_shares_grantee_idx ON agent_shares(grantee_user_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS agent_shares_agent_idx ON agent_shares(agent_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS agent_shares_open_unique
    ON agent_shares(agent_id, grantee_user_id)
    WHERE status IN ('pending', 'active');

CREATE TABLE IF NOT EXISTS agent_access_leases (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id         UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    agent_id          BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    subject_user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    owner_user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    share_id          UUID NULL REFERENCES agent_shares(id) ON DELETE CASCADE,
    policy_version    INTEGER NOT NULL DEFAULT 0,
    namespace         TEXT NOT NULL,
    issued_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at        TIMESTAMPTZ NOT NULL,
    revoked_at        TIMESTAMPTZ NULL,
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    request_id        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS agent_access_leases_active_idx
    ON agent_access_leases(agent_id, subject_user_id, expires_at)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS agent_access_leases_share_idx ON agent_access_leases(share_id, expires_at);

CREATE TABLE IF NOT EXISTS agent_share_usage_daily (
    share_id          UUID NOT NULL REFERENCES agent_shares(id) ON DELETE CASCADE,
    usage_date        DATE NOT NULL,
    tasks_created     INTEGER NOT NULL DEFAULT 0,
    bytes_uploaded    BIGINT NOT NULL DEFAULT 0,
    bytes_downloaded  BIGINT NOT NULL DEFAULT 0,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (share_id, usage_date)
);

CREATE TABLE IF NOT EXISTS agent_share_usage_events (
    share_id          UUID NOT NULL REFERENCES agent_shares(id) ON DELETE CASCADE,
    event_id          TEXT NOT NULL,
    usage_date        DATE NOT NULL,
    tasks_created     INTEGER NOT NULL DEFAULT 0 CHECK (tasks_created BETWEEN 0 AND 1),
    bytes_uploaded    BIGINT NOT NULL DEFAULT 0 CHECK (bytes_uploaded >= 0),
    bytes_downloaded  BIGINT NOT NULL DEFAULT 0 CHECK (bytes_downloaded >= 0),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (share_id, event_id)
);
CREATE INDEX IF NOT EXISTS agent_share_usage_events_date_idx
    ON agent_share_usage_events(usage_date, created_at DESC);

CREATE TABLE IF NOT EXISTS access_audit_log (
    id                BIGSERIAL PRIMARY KEY,
    actor_user_id     BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    action            TEXT NOT NULL,
    target_type       TEXT NOT NULL,
    target_id         TEXT NOT NULL DEFAULT '',
    owner_user_id     BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    grantee_user_id   BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    agent_id          BIGINT NULL REFERENCES agents(id) ON DELETE SET NULL,
    share_id          UUID NULL REFERENCES agent_shares(id) ON DELETE SET NULL,
    outcome           TEXT NOT NULL DEFAULT 'success' CHECK (outcome IN ('success', 'denied', 'failed')),
    reason            TEXT NOT NULL DEFAULT '',
    details           JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip_address        TEXT NOT NULL DEFAULT '',
    user_agent        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS access_audit_created_idx ON access_audit_log(created_at DESC);
CREATE INDEX IF NOT EXISTS access_audit_actor_idx ON access_audit_log(actor_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS access_audit_share_idx ON access_audit_log(share_id, created_at DESC);

INSERT INTO app_settings(key, value)
VALUES ('api_contract_version', '13'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

INSERT INTO schema_migrations(version, description)
VALUES (4, 'user lifecycle, agent sharing, access leases, usage and audit')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
