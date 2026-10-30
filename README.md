# relay-api

A microservice built with [wapgo](https://github.com/abdullahPrasetio/relay-api) — Clean Architecture, ENV-first config, and built-in observability.

## Quick start

```bash
# 1. Copy and configure environment variables
cp .env.example .env

# 2. Start dependencies (PostgreSQL)
make docker-up

# 3. Run the service
make run
```

## Enabled features

| Feature | Status |
|---|---|
| Database | PostgreSQL |
| Redis cache | — (add later with `wapgo add redis`) |
| Kafka | — (add later with `wapgo add kafka`) |
| RabbitMQ | — (add later with `wapgo add rabbitmq`) |
| Observability | disabled |

The service starts on `http://localhost:8080`.

```bash
curl http://localhost:8080/health
```

## Scheduled monitors

A monitor runs a synced `request` entity on a cron schedule. The API stores
definitions and run history; `cmd/monitor-worker` executes due rows in the
background. Runs go through the desktop app's extracted core bundle so variable
resolution, pre-request scripts and tests behave exactly like the app.

| Method | Path | Role |
|---|---|---|
| `GET` / `POST` | `/api/v1/workspaces/:id/monitors` | viewer / editor+ |
| `GET` / `PATCH` / `DELETE` | `/api/v1/monitors/:id` | viewer / editor+ / editor+ |
| `GET` | `/api/v1/monitors/:id/runs?limit=50` | viewer+ |
| `POST` | `/api/v1/monitors/:id/run` | editor+ (synchronous manual run) |

Create body: `{name, requestId, cron, enabled?, alertWebhook?}`; `requestId`
must be a live `kind='request'` entity in the workspace. PATCH accepts the same
fields and `alertWebhook: null` clears it. Cron expressions are standard
five-field cron (or `@daily`/`@hourly`), evaluated in UTC.

Worker configuration (see `.env.example`):

| Variable | Default | Description |
|---|---|---|
| `RELAY_CORE_PATH` | cwd | Directory containing `dist-core/relay-core.mjs` |
| `MONITOR_RUNNER_CMD` | `node dist-core/relay-core.mjs` | Runner command, split on whitespace, no shell |
| `MONITOR_RUNNER_TIMEOUT` | `60s` | Per-run subprocess timeout |
| `MONITOR_POLL_INTERVAL` | `5s` | Claim loop interval |
| `MONITOR_BATCH_SIZE` | `10` | Monitors claimed per tick |

Core CLI contract (stdin/stdout, also used by the app build):

```text
stdin : {"workspace": {id,name,collections,environments,globals,activeEnvId},
         "requestId": "req_…", "environmentId": "env_…" | null}
stdout: {"response": {"status": 200, "headers": [{"key","value"}],
                      "body": "…", "timeMs": 12} | null,
         "testResults": [{"name","passed","error"?}],
         "error": null | "message"}
exit  : 0 executed (4xx/5xx and failed assertions still 0), 1 execution
        error, 2 invalid stdin / request or environment not found
```

A run is healthy when the CLI reports no error, every test passed, and the HTTP
status is `< 400`. The worker stores the run before sending the failure alert
(`{monitor, run, workspaceId}` POST, exponential backoff) so an unreachable
webhook never loses history. Alerts fire once per ok→fail transition.

## Generate a new domain

```bash
# Scaffold all layers (entity, repo, usecase, handler, route, http client)
wapgo make:all <name>

# Or generate individual layers
wapgo make:model    <name>
wapgo make:repo     <name>
wapgo make:usecase  <name>
wapgo make:controller <name>
wapgo make:route    <name>
wapgo make:client   <name>

# Generate a SQL migration file pair
wapgo make:migration <name>
```

After generating, wire the new handler and repository in `cmd/api/main.go` and register the route in `internal/delivery/http/route/router.go`.

## Available make targets

| Target | Description |
|---|---|
| `make run` | Run the service locally |
| `make build` | Build the binary to `bin/api` |
| `make test` | Run unit tests |
| `make test-race` | Run tests with race detector |
| `make coverage` | Run tests and check coverage ≥ 80% |
| `make lint` | Run golangci-lint |
| `make check` | lint + sec + test-race + coverage |
| `make docker-up` | Start all dependencies via docker-compose |
| `make docker-down` | Stop all dependencies |
| `make tidy` | Run go mod tidy for all modules |

## Project structure

```
.
├── cmd/api/main.go              ← dependency wiring + entrypoint
├── config/                      ← Viper ENV-first config
├── internal/
│   ├── domain/                  ← entities, repository interfaces, service interfaces
│   ├── usecase/                 ← business logic
│   ├── delivery/http/           ← handlers, middleware, routes
│   └── repository/              ← GORM (db/) and Redis (redis/) implementations
├── pkg/
│   ├── auth/                    ← JWT middleware
│   ├── httpclient/              ← resilient inter-service HTTP client
│   ├── messaging/               ← Kafka and RabbitMQ producers/consumers
│   ├── observability/           ← OpenTelemetry / Elastic APM provider
│   ├── logger/                  ← zerolog structured logging
│   └── response/                ← standard response helpers (Success, Paginated, Error…)
├── migrations/                  ← SQL migration files
└── .env.example                 ← environment variable reference
```

## Configuration

All configuration is via environment variables (see `.env.example`).
Key variables:

| Variable | Default | Description |
|---|---|---|
| `APP_ENV` | `development` | `development` or `production` |
| `APP_PORT` | `8080` | HTTP listen port |
| `DB_DRIVER` | `postgres` | `postgres` or `mysql` |
| `OBSERVABILITY_PROVIDER` | `none` | `otel`, `elastic_apm`, or `none` |

## Module

```
github.com/abdullahPrasetio/relay-api
```
