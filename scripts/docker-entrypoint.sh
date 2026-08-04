#!/bin/sh
set -eu

: "${POSTGRES_HOST:=postgres}"
: "${POSTGRES_PORT:=5432}"
: "${POSTGRES_DB:=echoear}"
: "${POSTGRES_USER:=echoear}"
: "${BOOTSTRAP_ADMIN_USERNAME:=}"
: "${BOOTSTRAP_ADMIN_PASSWORD:=}"
: "${BOOTSTRAP_ADMIN_EMAIL:=}"

if [ -z "$BOOTSTRAP_ADMIN_PASSWORD" ] || [ "$BOOTSTRAP_ADMIN_PASSWORD" = "change-this-admin-password" ]; then
  echo "BOOTSTRAP_ADMIN_PASSWORD must be set to a non-placeholder value" >&2
  exit 1
fi

until pg_isready -q -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$POSTGRES_DB"; do
  sleep 1
done

for migration in /app/migrations/*.sql; do
  PGPASSWORD="${POSTGRES_PASSWORD:-}" psql \
    -q \
    -v ON_ERROR_STOP=1 \
    -v bootstrap_admin_username="$BOOTSTRAP_ADMIN_USERNAME" \
    -v bootstrap_admin_password="$BOOTSTRAP_ADMIN_PASSWORD" \
    -v bootstrap_admin_email="$BOOTSTRAP_ADMIN_EMAIL" \
    -h "$POSTGRES_HOST" \
    -p "$POSTGRES_PORT" \
    -U "$POSTGRES_USER" \
    -d "$POSTGRES_DB" \
    -f "$migration"
done

exec /app/echoear-cloud
