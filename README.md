# relay-api

[![CI](https://github.com/abdullahPrasetio/relay-api/actions/workflows/ci.yml/badge.svg)](https://github.com/abdullahPrasetio/relay-api/actions/workflows/ci.yml)

Backend for Relay, an API client with offline-first team collaboration. It owns
accounts and device sessions, team workspaces with role-based access, and
conflict-aware delta sync for collections, folders, requests, environments and
globals. Monitors are planned.

## Stack

- **Go 1.25**, [Fiber v2](https://gofiber.io) HTTP server
- **PostgreSQL** via GORM, with embedded versioned SQL migrations
  ([golang-migrate](https://github.com/golang-migrate/migrate))
- **Auth**: HS256 access JWTs plus rotating opaque refresh tokens (per-device
  sessions); passwords hashed with bcrypt
- **Observability**: zerolog structured logs, Prometheus metrics, optional
  OpenTelemetry or Elastic APM
- Docker Compose for local Postgres

## Quick start

```bash
cp .env.example .env
# Edit .env: point DB_* at Postgres and add JWT_SECRET (at least 32 bytes).

make docker-up   # local Postgres 16 (skip if you already have one)
make migrate     # apply migrations/*.sql
make run         # http://localhost:8080
```

Generate a signing secret with `openssl rand -base64 48`.

```bash
curl http://localhost:8080/health
```

`GET /health` returns `200` while the database is reachable and `503` with
`"status": "degraded"` otherwise.

## Required environment

Configuration is environment-first (a `.env` file is loaded when present).
See `.env.example` for the common defaults; add `JWT_SECRET` yourself.

| Variable | Required | Notes |
|---|---|---|
| `DB_DRIVER` | yes | `postgres` (default) or `mysql` |
| `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER` | yes | Database connection |
| `DB_PASSWORD` | yes | May be empty when the server uses trust auth |
| `DB_SSL_MODE` | production | `disable` locally, `require` in production |
| `JWT_SECRET` | yes | At least 32 bytes; token signing fails below that |
| `APP_ENV`, `APP_PORT` | no | Default `development`, `8080`; `/metrics` is 404 in `production` |
| `JWT_ACCESS_TTL`, `JWT_REFRESH_TTL`, `JWT_REFRESH_GRACE` | no | Defaults `15m`, `720h`, `120s` |
| `REDIS_URL` | optional | Parsed by config; no Redis-backed feature is wired yet |
| `OBSERVABILITY_PROVIDER` | optional | `none` (default in `.env.example`), `otel`, `elastic_apm` |
| `LOG_LEVEL`, `LOG_DIR`, `LOG_HTTP_BODIES` | no | Structured logging controls |

`DB_AUTO_MIGRATE` exists for local development only; it is rejected when
`APP_ENV=production`. Use `cmd/migrate` for every real schema change.

## API overview

All product routes are under `/api/v1`; workspace and account routes require
the `Authorization: Bearer` header carrying the access token from login or
refresh.

| Area | Endpoints | Access |
|---|---|---|
| Health | `GET /health` | Public |
| Auth | `POST /api/v1/auth/register`, `/login`, `/refresh`, `/logout` | Public |
| Account | `GET /api/v1/auth/me`, `GET /api/v1/auth/devices`, `DELETE /api/v1/auth/devices/:id` | Bearer |
| Workspaces | `GET /api/v1/workspaces`, `POST /api/v1/workspaces`, `GET /api/v1/workspaces/:id`, `PATCH /api/v1/workspaces/:id`, `DELETE /api/v1/workspaces/:id` | Bearer; rename needs admin+, delete needs owner |
| Members | `GET /api/v1/workspaces/:id/members`, `POST /api/v1/workspaces/:id/members`, `PATCH /api/v1/workspaces/:id/members/:userId`, `DELETE /api/v1/workspaces/:id/members/:userId` | Bearer; members list, owner/admin manage |
| Changes | `GET /api/v1/workspaces/:id/changes?since=0&limit=500`, `POST /api/v1/workspaces/:id/changes` | Bearer; viewer+ pull, editor+ push |
| Metrics | `GET /metrics` | Public outside production |

The original scaffold's generic user CRUD (`GET`/`POST /api/v1/users` and
`GET`/`PUT`/`DELETE /api/v1/users/:id`) is still mounted publicly; it is not
part of the Relay client surface.

Workspace IDs are opaque `ws_…` strings. Roles are `owner`, `admin`, `editor`
and `viewer`; a workspace the caller cannot see is reported as `404`, not
`403`, so membership does not leak.

### Delta sync

Push a batch of at most 200 operations. `opId` makes retries idempotent: the
stored result is returned without appending a second change. `baseRev` is an
optimistic concurrency check (`0` means create); stale writes come back with
`"conflict": true` and the server's current `rev`/payload.

```json
{
  "changes": [
    {
      "opId": "op-1",
      "id": "req_1",
      "kind": "request",
      "baseRev": 0,
      "payload": {"name": "List users", "method": "GET"}
    }
  ]
}
```

```json
{"results":[{"opId":"op-1","applied":true}],"serverSeq":42}
```

Pull with the last applied cursor. The response contains `changes`, `nextSeq`
and `hasMore`; `limit` defaults to 500 and is capped at 1000. Entity kinds are
`collection`, `folder`, `request`, `environment` and `globals`. The pull log is
append-only and is never pruned; only the `entity_ops` idempotency log has a
retention window (`cmd/prune`, 90 days by default).

## Testing

```bash
go test ./...            # unit tests
go test -race ./...      # unit tests with the race detector
go vet ./...
```

Integration tests need a disposable PostgreSQL database. They drop and
recreate the `public` schema, so never point them at real data.

```bash
export TEST_DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/relay_test?sslmode=${DB_SSL_MODE:-disable}"
export MIGRATE_TEST_DSN="$TEST_DATABASE_URL"   # optional: migration test skips without it
go test -p 1 -count=1 -tags=integration ./...
```

`-p 1` is required because the integration packages reset the same schema and
would race under Go's default package parallelism.

CI (`.github/workflows/ci.yml`) runs `go build`, `go vet` and `go test -race`,
applies migrations and runs the integration suite against PostgreSQL 16, and
runs gosec plus golangci-lint on every push and pull request.

## Migrations

Migrations are embedded from `migrations/` and applied by a CLI, so the same
migration set runs locally and in a release pipeline.

```bash
make migrate                  # apply all pending
make migrate-down             # revert one step
make migrate-version          # current version + dirty flag
make migrate-force V=<n>      # clear a dirty state at version n
```

Add a change as `migrations/NNNNNN_name.up.sql` and the matching
`.down.sql`. Never edit an applied migration; add a new pair instead. In
production run `go run ./cmd/migrate -dir up` as a deploy step with
`DB_SSL_MODE=require`.

## Project layout

```text
cmd/api                    HTTP entrypoint and dependency wiring
cmd/migrate                embedded-migration CLI (up/down/version/force)
cmd/prune                  retention job for the entity_ops idempotency log
config                     environment-first configuration loading
internal/domain            entities, repository and service interfaces
internal/usecase           auth, users, workspaces and delta-sync logic
internal/repository/db     GORM implementations
internal/delivery/http     Fiber handlers, middleware and routes
internal/integration       PostgreSQL-backed integration tests
migrations                 embedded SQL migration pairs
pkg/auth                   JWT, token sealing, auth middleware
pkg/database               connection setup and pooling
pkg/httpclient             resilient, SSRF-safe outbound HTTP client
pkg/journal                request/response journal and redaction
pkg/logger                 zerolog setup and file sinks
pkg/observability          Prometheus, OpenTelemetry and Elastic APM
pkg/response               response envelope helpers
pkg/validator              request validation wrapper
```
