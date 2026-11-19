# go-task-management-api

Task management REST API: Go + Echo v4, PostgreSQL 17, pgx/v5 (SQL-first, no
ORM), golang-migrate, layered architecture with manual dependency injection.
See [PROJECT_CONTEXT.md](PROJECT_CONTEXT.md) for the canonical design,
policies, and implementation status.

## Requirements

- Go 1.27+
- PostgreSQL 17 (a local Docker container `task-management-db` on
  `127.0.0.1:5432` is the reference development database)
- Docker + Compose (optional, for the containerized stack)

## Configuration

Copy `.env.example` to `.env` (gitignored) and fill in real values.
Environment variables always take precedence over `.env`.

Required secrets — generate locally, never commit, never print or log:

- `DB_PASSWORD` — must equal the database container's `POSTGRES_PASSWORD`.
- `AUTH_ACCESS_SECRET` — HS256 signing secret, **at least 32 bytes**, e.g.
  `openssl rand -base64 48`.

Other required variables (no defaults): `DB_NAME`, `DB_USER`, `DB_SSLMODE`
(`disable` for the local container only; production must use `verify-full`).
Defaults: `HTTP_PORT=8080`, timeouts `5s/10s/60s`, pool `10/0/30m`,
`AUTH_ACCESS_TTL=15m`, `AUTH_REFRESH_TTL=720h`, `AUTH_RATE_LIMIT_INTERVAL=1s`,
`AUTH_CLEANUP_INTERVAL=1h`.
Invalid configuration fails startup with variable names and safe explanations only.

## Security behavior

- **Auth rate limiting:** `register`, `login`, `refresh`, and `change-password` are
  rate-limited per client IP (token bucket: burst 5, then one request per
  `AUTH_RATE_LIMIT_INTERVAL`). All four endpoints **share one bucket per IP**. Excess
  requests get `429` with a `Retry-After` header. The limiter uses the connection peer
  address and ignores forwarding headers; memory is bounded with bounded-cost sampled
  eviction. Limits are **per instance** — multi-replica deployments should also enforce
  limits at the reverse proxy / shared gateway.
- **Optimistic concurrency:** `PATCH` updates to users, projects, and tasks succeed only
  if the record's `updated_at` still matches the value read at the start of the request.
  A concurrent modification returns `409 conflict` (retry with fresh state); a deleted
  record returns `404`. Omitted fields are merged in the service on its freshest read, so
  a partial update never reverts a field changed concurrently. No extra column or
  migration is involved.
- **Login timing equalization:** an unknown email performs one Argon2id verification
  against a once-generated dummy hash, so it costs the same as a wrong password and
  returns the identical `401`. This prevents user enumeration via response timing.
- **Refresh-session cleanup:** a background worker drains expired/revoked rows older
  than the 30-day retention window in bounded 500-row batches on a configurable tick
  (`AUTH_CLEANUP_INTERVAL`, default 1h). Passes never overlap within an instance;
  shutdown cancels and joins the worker before the pool closes. Multi-replica
  deployments should coordinate cleanup externally (e.g. a single scheduled runner).

## Run locally

```sh
# 1. Apply migrations (explicit step; the API does not auto-migrate)
go run ./cmd/migrate

# 2. Start the API
go run ./cmd/api
```

- Liveness: `GET /health` · Readiness (DB ping): `GET /ready`
- Swagger UI: `http://localhost:8080/swagger/index.html`
- Graceful shutdown on SIGINT/SIGTERM (10s bounded drain; the cleanup worker is cancelled and joined before pool close).

## Run with Docker Compose

The compose stack builds the app image and runs `migrate` to completion
before starting the API. It does **not** define a database service: the
existing `task-management-db` container, its volume, and port binding are
preserved and reached via `host.docker.internal`.

```sh
export AUTH_ACCESS_SECRET="$(openssl rand -base64 48)"  # DB_PASSWORD comes from .env
docker compose up --build
```

The API is published on `127.0.0.1:8080` only. The image runs as a non-root
user and never contains `.env` (excluded by `.dockerignore`).

## Regenerate the OpenAPI spec

After changing handler annotations:

```sh
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g internal/handler/doc.go -o docs
```

## Tests and checks

```sh
gofmt -l .
go test -count=1 ./...        # unit tests — fully offline, no Docker needed
go vet ./...
go build ./...
```

Integration tests (T1–T7: real PostgreSQL foreign keys, cascades, concurrent
refresh-token rotation/replay, cleanup batches and boundaries, optimistic-
concurrency conflict/not-found/strict-advance behavior, and partial-merge
preservation of omitted fields) live in
`internal/integration` behind the `integration` build tag and require Docker:

```sh
go test -count=1 -tags=integration ./internal/integration/...
```

They start an **isolated temporary** `postgres:17-alpine` container via
Testcontainers, apply the embedded migrations with the real runner, and clean up
afterwards. They never connect to the development database (`task_management` /
`task-management-db`) and never read `.env`.

## CI

`.github/workflows/ci.yml` runs on pushes to `main` and on pull requests:
a unit job (gofmt check, `go vet`, `go build`, offline unit tests) and an
integration job (tagged T1–T7 suite using the GitHub-hosted runner's Docker).
The Go version is taken from `go.mod`; no secrets or external databases are used.