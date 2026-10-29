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
