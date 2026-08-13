#!/bin/sh
set -eu

for command in initdb pg_ctl createdb psql; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required" >&2
    exit 1
  fi
done

ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/echoear-cloud-migrations.XXXXXX")
DATA_DIR="$WORK_DIR/data"
SOCKET_DIR="$WORK_DIR/socket"
PORT=${TEST_POSTGRES_PORT:-55432}
mkdir -p "$SOCKET_DIR"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  pg_ctl -D "$DATA_DIR" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$WORK_DIR"
  exit "$status"
}
trap cleanup EXIT INT TERM

initdb -A trust -U postgres -D "$DATA_DIR" >/dev/null
pg_ctl -D "$DATA_DIR" -o "-F -p $PORT -k $SOCKET_DIR" -w start >/dev/null
createdb -h "$SOCKET_DIR" -p "$PORT" -U postgres echoear_migration_test

PSQL="psql -X -q -h $SOCKET_DIR -p $PORT -U postgres -d echoear_migration_test"

# Reproduce an installation whose users table predates the role column.
$PSQL <<'SQL'
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE users (
    id                BIGSERIAL PRIMARY KEY,
    username          TEXT NOT NULL,
    password_hash     TEXT NOT NULL,
    email             TEXT NOT NULL DEFAULT '',
    password_changed  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at     TIMESTAMPTZ NULL
);
INSERT INTO users(username, password_hash, email)
VALUES ('existing-user', crypt('existing-password', gen_salt('bf', 4)), 'existing@example.com');
SQL

run_migrations() {
  for migration in "$ROOT"/migrations/*.sql; do
    $PSQL \
      -v ON_ERROR_STOP=1 \
      -v bootstrap_admin_username="bootstrap-admin" \
      -v bootstrap_admin_password="bootstrap-password" \
      -v bootstrap_admin_email="bootstrap@example.com" \
      -f "$migration"
  done
}

run_migrations
run_migrations

summary=$($PSQL -Atc "
SELECT
    (SELECT COUNT(*) FROM users),
    (SELECT COUNT(*) FROM users WHERE role = 'admin'),
    (SELECT COUNT(*) FROM user_settings),
    (SELECT COUNT(*) FROM app_settings),
    (SELECT COUNT(*) FROM schema_migrations),
    (SELECT value #>> '{}' FROM app_settings WHERE key = 'api_contract_version'),
    (SELECT STRING_AGG(username || ':' || role, ',' ORDER BY id) FROM users);
")

expected="1|1|1|3|5|14|existing-user:admin"
if [ "$summary" != "$expected" ]; then
  echo "unexpected migration state: $summary" >&2
  exit 1
fi

table_count=$($PSQL -Atc "
SELECT COUNT(*)
FROM unnest(ARRAY[
    'schema_migrations', 'app_settings', 'users', 'user_sessions', 'devices',
    'agents', 'hapi_connection_requests', 'hapi_connection_responses',
	'user_settings', 'pairing_claims', 'agent_shares', 'agent_access_leases',
	'agent_share_usage_daily', 'agent_share_usage_events', 'access_audit_log'
]) AS expected_table(name)
WHERE to_regclass(name) IS NOT NULL;
")

if [ "$table_count" != "15" ]; then
	echo "expected 15 application tables, found $table_count" >&2
  exit 1
fi

ECHOEAR_TEST_DATABASE_URL="host=$SOCKET_DIR port=$PORT user=postgres dbname=echoear_migration_test sslmode=disable" \
  go test "$ROOT/internal/database" -run TestCreateAndAcceptSharePostgresIntegration -count=1

echo "migration replay passed: $summary; tables=$table_count"
