# EchoEar Cloud

Standalone EchoEar Desk cloud service. It owns its authentication, device and
Agent directory, encrypted HAPI connection mailbox, and encrypted WebSocket
tunnel. It has no runtime dependency on `mqtt_server`.

## Components

- Go/Gin API under `/api/v1`
- PostgreSQL 17
- Idempotent SQL migrations in `migrations/`
- Docker Compose deployment for API and PostgreSQL
- End-to-end encrypted HAPI relay: the service only routes opaque frames

The clean schema intentionally excludes the retired ACP task/message history
tables. HAPI remains the source of truth for tasks, messages, models, tools,
permissions and session history.

## Start

```bash
cp .env.example .env
# Set unique POSTGRES_PASSWORD and BOOTSTRAP_ADMIN_PASSWORD values in .env.
docker compose up -d --build
docker compose ps
curl http://127.0.0.1:8080/healthz
```

`migrations/*.sql` run before every API start. They use idempotent DDL, so both
a new PostgreSQL volume and an existing deployment receive all required tables
and default rows. The bootstrap administrator is inserted only when that
username does not already exist; restarting a container never resets its
password.

The default API port is `8080`. Put a TLS reverse proxy in front of it for an
Internet deployment and set `PUBLIC_BASE_URL` to the exact origin users enter
in Android and macOS, for example `https://echoear.example.com`. Startup fails
when this value is absent or contains an API path, preventing empty or invalid
cloud addresses from being provisioned into devices.

## Required configuration

| Variable | Purpose |
| --- | --- |
| `POSTGRES_PASSWORD` | PostgreSQL password; required |
| `BOOTSTRAP_ADMIN_PASSWORD` | Initial standalone admin password; required |
| `BOOTSTRAP_ADMIN_USERNAME` | Initial username, default `admin` |
| `PUBLIC_BASE_URL` | Public origin returned during device binding; required |
| `CORS_ALLOWED_ORIGINS` | Comma-separated origins; native-only deployments may use `*` |
| `HTTP_PORT` | Host port published by Compose, default `8080` |

Do not copy the old `mqtt_server` database into this service. Accounts and
sessions are independent. Existing users must be created in EchoEar Cloud or
migrated by an explicit one-time data migration before clients are switched.

## API

- `POST /api/v1/auth/login`
- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/me`
- `PUT /api/v1/auth/password`
- `/api/v1/echoear/devices/*`
- `/api/v1/echoear/agents/*`
- `/api/v1/echoear/settings`
- `/api/v1/echoear/pairing/*`

Authentication retains the native-client header format:

```text
Authorization: Session <session_id>
```

Device presence uses a separate short-lived bearer access token. Session and
device tokens are stored as SHA-256 hashes; passwords use bcrypt.

## Operations

```bash
# Logs
docker compose logs -f api

# Database backup
docker compose exec -T postgres pg_dump -U echoear echoear > echoear.sql

# Stop without deleting data
docker compose down
```

Do not add `-v` to the last command unless the PostgreSQL data volume is
intentionally being destroyed.
