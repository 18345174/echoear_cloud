\set ON_ERROR_STOP on

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version       INTEGER PRIMARY KEY,
    description   TEXT NOT NULL,
    applied_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app_settings (
    key           TEXT PRIMARY KEY,
    value         JSONB NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS users (
    id                BIGSERIAL PRIMARY KEY,
    username          TEXT NOT NULL,
    password_hash     TEXT NOT NULL,
    email             TEXT NOT NULL DEFAULT '',
    role              TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    password_changed  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at     TIMESTAMPTZ NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS users_username_lower_unique ON users (LOWER(username));

CREATE TABLE IF NOT EXISTS user_sessions (
    id               BIGSERIAL PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_hash     TEXT NOT NULL UNIQUE,
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired')),
    ip_address       TEXT NOT NULL DEFAULT '',
    user_agent       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    revoked_at       TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS user_sessions_user_status_idx ON user_sessions(user_id, status);
CREATE INDEX IF NOT EXISTS user_sessions_expiry_idx ON user_sessions(expires_at);

CREATE TABLE IF NOT EXISTS devices (
    id                         BIGSERIAL PRIMARY KEY,
    user_id                    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_uid                 TEXT NOT NULL UNIQUE,
    display_name               TEXT NOT NULL DEFAULT '',
    hostname                   TEXT NOT NULL DEFAULT '',
    fw_version                 TEXT NOT NULL DEFAULT '',
    last_ip                    INET NULL,
    last_seen_at               TIMESTAMPTZ NULL,
    bind_state                 TEXT NOT NULL DEFAULT 'active' CHECK (bind_state IN ('pending', 'active', 'revoked')),
    pair_public_id             TEXT NOT NULL DEFAULT '',
    pair_secret_hash           TEXT NULL,
    capabilities               JSONB NOT NULL DEFAULT '{}'::jsonb,
    lan_hint                   JSONB NOT NULL DEFAULT '{}'::jsonb,
    device_refresh_token_hash  TEXT NULL,
    device_refresh_expires_at  TIMESTAMPTZ NULL,
    device_access_token_hash   TEXT NULL,
    device_access_expires_at   TIMESTAMPTZ NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at                 TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS devices_user_state_idx ON devices(user_id, bind_state);
CREATE INDEX IF NOT EXISTS devices_last_seen_idx ON devices(last_seen_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS devices_access_hash_idx ON devices(device_access_token_hash)
    WHERE device_access_token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS agents (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id              TEXT NOT NULL,
    host_name             TEXT NOT NULL DEFAULT '',
    platform              TEXT NOT NULL DEFAULT '',
    app_version           TEXT NOT NULL DEFAULT '',
    preferred_device_uid  TEXT NULL,
    last_seen_at          TIMESTAMPTZ NULL,
    lan_base_url          TEXT NOT NULL DEFAULT '',
    public_key            TEXT NOT NULL DEFAULT '',
    key_id                TEXT NOT NULL DEFAULT '',
    key_algorithm         TEXT NOT NULL DEFAULT '',
    capabilities          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, agent_id)
);
CREATE INDEX IF NOT EXISTS agents_user_idx ON agents(user_id);

CREATE TABLE IF NOT EXISTS hapi_connection_requests (
    id             BIGSERIAL PRIMARY KEY,
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id       TEXT NOT NULL,
    request_id     TEXT NOT NULL,
    encrypted_data JSONB NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'claimed', 'completed', 'rejected', 'expired')),
    result          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '2 minutes'),
    claimed_at      TIMESTAMPTZ NULL,
    completed_at    TIMESTAMPTZ NULL,
    UNIQUE(user_id, agent_id, request_id)
);
CREATE INDEX IF NOT EXISTS hapi_connection_requests_pending_idx
    ON hapi_connection_requests(user_id, agent_id, status, id) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS hapi_connection_responses (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id          TEXT NOT NULL,
    request_id        TEXT NOT NULL,
    encrypted_payload JSONB NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at        TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '5 minutes'),
    UNIQUE(user_id, agent_id, request_id)
);
CREATE INDEX IF NOT EXISTS hapi_connection_responses_expiry_idx ON hapi_connection_responses(expires_at);

CREATE TABLE IF NOT EXISTS user_settings (
    user_id         BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    notify_enabled  BOOLEAN NOT NULL DEFAULT TRUE,
    locale          TEXT NOT NULL DEFAULT 'zh-CN',
    stt_preference  TEXT NOT NULL DEFAULT 'cloud_default',
    extra_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS pairing_claims (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_uid            TEXT NOT NULL,
    claim_code            TEXT NOT NULL UNIQUE,
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ NULL,
    consumed_by_agent_id  TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS pairing_claims_user_idx ON pairing_claims(user_id);
CREATE INDEX IF NOT EXISTS pairing_claims_active_idx ON pairing_claims(claim_code) WHERE consumed_at IS NULL;

INSERT INTO schema_migrations(version, description)
VALUES (1, 'standalone EchoEar cloud schema')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
