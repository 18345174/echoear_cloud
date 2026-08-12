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
- CI-published multi-arch container image on GitHub Container Registry (GHCR)
- Optional OCI auto-deploy after each successful image publish
- Optional self-hosted HAPI Relay deployment with per-Hub issued credentials

The clean schema intentionally excludes the retired ACP task/message history
tables. HAPI remains the source of truth for tasks, messages, models, tools,
permissions and session history.

## Container image

Every push to `main` builds and publishes a multi-arch API image
(`linux/amd64` + `linux/arm64`). Full reference:
[docs/docker-image.md](docs/docker-image.md).

| Item | Value |
| --- | --- |
| Image | `ghcr.io/18345174/echoear_cloud` |
| Latest tag | `ghcr.io/18345174/echoear_cloud:latest` |
| Platforms | `linux/amd64`, `linux/arm64` |
| Packages | https://github.com/18345174/echoear_cloud/pkgs/container/echoear_cloud |
| Publish workflow | [Publish Docker image](https://github.com/18345174/echoear_cloud/actions/workflows/docker-publish.yml) |
| Deploy workflow | [Deploy OCI](https://github.com/18345174/echoear_cloud/actions/workflows/deploy-oci.yml) |

```bash
# Pull the latest published API image
docker pull ghcr.io/18345174/echoear_cloud:latest
```

`docker-compose.yml` already points at that image (`pull_policy: always`), so a
normal compose deploy downloads the cloud build instead of compiling locally.

Other tags pushed on each build: `sha-<short>`, `sha-<full>`, and UTC timestamp
(`YYYYMMDD-HHmmss`). Prefer a SHA tag when you need a pinned production deploy.

## OCI auto-deploy

After the image is published, the publish workflow **actively dispatches** the
independent **Deploy OCI** workflow (it does not use `workflow_run` watching).

Server layout, dedicated SSH deploy key, and secrets:
[deploy/oci/README.md](deploy/oci/README.md).

Server path: `/opt/stack/echoear_cloud`  
Remote command: `/opt/stack/echoear_cloud/deploy.sh`  
Local health on server: `http://127.0.0.1:18080/healthz`

After a one-time server bootstrap, the deployment script also refreshes its
versioned Compose manifest from this repository. The optional Relay remains
disabled until its DNS, firewall rules, persisted state, and server-only secret
are configured. See [deploy/oci/README.md](deploy/oci/README.md).

## Start

```bash
cp .env.example .env
# Set unique passwords. ACCESS_TICKET_SIGNING_KEY may be left empty to let
# PostgreSQL generate and persist one value, or override it with: openssl rand -base64 32
docker compose pull
docker compose up -d
docker compose ps
curl http://127.0.0.1:8080/healthz
```

`migrations/*.sql` run before every API start. They use idempotent DDL, so both
a new PostgreSQL volume and an existing deployment receive all required tables
and default rows. When an existing deployment has users but no administrator,
the earliest account is promoted to `admin`; otherwise a bootstrap administrator
is inserted only when no administrator exists. Restarting a container never
duplicates users or settings and never resets a password.

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
| `ACCESS_TICKET_SIGNING_KEY` | Optional base64-encoded 32-byte Ed25519 seed override; otherwise persisted in `app_settings` |
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
- `POST /api/v1/auth/register` (administrator only)
- `/api/v1/echoear/devices/*`
- `/api/v1/echoear/agents/*`
- `/api/v1/echoear/settings`
- `/api/v1/echoear/pairing/*`

Authentication retains the native-client header format:

```text
Authorization: Session <session_id>
```

Accounts have either the `user` or `admin` role. Registration defaults to
`user`; an administrator may explicitly create another administrator:

```json
{
  "username": "new-user",
  "password": "initial-password",
  "email": "user@example.com",
  "role": "user"
}
```

Device presence uses a separate short-lived bearer access token. Session and
device tokens are stored as SHA-256 hashes; passwords use bcrypt.

## Operations

```bash
# Logs
docker compose logs -f api

# Refresh API container to the newest GHCR image
docker compose pull api
docker compose up -d api

# Database backup
docker compose exec -T postgres pg_dump -U echoear echoear > echoear.sql

# Stop without deleting data
docker compose down
```

Do not add `-v` to the last command unless the PostgreSQL data volume is
intentionally being destroyed.
