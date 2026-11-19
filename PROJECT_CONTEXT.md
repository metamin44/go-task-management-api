# PROJECT_CONTEXT — go-task-management-api

> Canonical context for future Qoder tasks. Captures finalized design decisions and the
> current implementation state. All application layers are implemented (domain, repository,
> service, handler) plus the composition root (`cmd/api`), and **migrations HAVE been applied**
> to PostgreSQL (`schema_migrations` version=5, dirty=false). Remaining work: deployment
> hardening (the scheduled cleanup job is now wired). Integration tests T1–T7 are implemented
> (Testcontainers, `integration` build tag). Per-IP authentication rate limiting and
> optimistic concurrency (`updated_at` version predicate) on user/project/task updates
> are implemented. Sections marked *pending* are
> future work, not completed features.
> Never read, print, copy, or commit the secret value in `.env`.

## 1. Project overview and MVP scope

A task management REST API in Go: user accounts with token-based authentication, projects
with role-based membership, and tasks with assignment, status, priority, and due dates.

MVP scope (from finalized requirements; implemented unless marked pending):

- User registration/login; access + refresh token auth with rotation and reuse detection.
- Session management: one active refresh token per session, session revocation, logout.
- Projects: creation atomically with owner membership; roles `owner` / `admin` / `member`;
  transactional owner transfer; at most one owner per project (DB-enforced), at least one
  owner (app-enforced).
- Tasks: CRUD within projects; nullable assignee who must be a member of the same project;
  statuses `todo` / `in_progress` / `done`; priorities `low` / `medium` / `high` / `urgent`.
- Scheduled cleanup of expired/revoked refresh token records (30-day retention) —
  **implemented** (`service.CleanupWorker`, configurable `AUTH_CLEANUP_INTERVAL`, default 1h;
  bounded 500-row batches via `DeleteExpiredBatch`, no overlapping passes; see §12).

## 2. Confirmed technology stack

| Concern | Decision |
|---|---|
| Language | Go |
| HTTP framework | Echo |
| Database | PostgreSQL 17 (running: 17.12, `postgres:17-alpine` in Docker) |
| DB driver | `pgx/v5` + `pgxpool` |
| Migrations | `golang-migrate` (SQL files, up/down) |
| Data access | SQL-first, **no ORM** |
| Architecture | Layered architecture, **manual dependency injection** |
| UUIDs | `gen_random_uuid()` (v4, core since PG13); app-side UUIDv7 optional later |
| Timestamps | `timestamptz` everywhere |
| Enums | `text` + `CHECK` constraints (not native enums) |

## 3. Layered architecture and planned directory structure

Layers: HTTP handlers (Echo) → services (business rules, transactions) → repositories
(pgx SQL) → PostgreSQL. Domain types are shared; dependencies are wired manually in `main`.
Transactions and row locking live in the service layer.

Current structure (status per package):

```
cmd/api/                   # implemented: composition root (config→logger→pool→DI→Echo,
                           #   graceful shutdown, /ready DB ping, /swagger/* UI)
cmd/migrate/               # implemented: applies embedded migrations, secret-safe errors
internal/config/           # implemented: Load/validate/DSN incl. AUTH_* settings + tests
internal/database/         # implemented: NewPool(ctx, cfg) pgxpool bootstrap + tests
internal/migration/        # implemented: Run(cfg, fs.FS) via iofs + pgx5 driver
internal/domain/           # implemented: entities, enums, validation, UUID, sentinels,
                           #   repository interfaces + Transactor + tests
internal/repository/       # implemented: SQL-first pgx repositories (5), ctx-based tx,
                           #   error mapping (incl. 40P01/40001), query builder + tests
internal/service/          # implemented: auth/users/projects/members/tasks services,
                           #   argon2id, JWT, rotation/reuse, retries + offline tests
internal/handler/          # implemented: handler.go (API, service interfaces, context
                           #   helpers), router.go (Options + route/group registration
                           #   and middleware wiring), per-resource handler files, DTOs,
                           #   middleware (request ID, logging, recovery, JWT auth, CORS
                           #   whitelist), Swagger annotations + offline HTTP tests
                           #   (incl. route-table regression guard)
internal/integration/      # implemented: T1–T7 live-PostgreSQL suite (build tag
                           #   "integration"; Testcontainers harness, real migrations)
docs/                      # generated OpenAPI spec (swag init; docs.go/swagger.json/yaml)
migrations/                # 000001–000005 up+down pairs + embed.go; APPLIED (version=5)
.github/workflows/ci.yml   # CI: gofmt/vet/build/unit + tagged integration job
Dockerfile                 # multi-stage, non-root, healthcheck; .env excluded
docker-compose.yml         # migrate one-shot + api only; existing DB container preserved
.env.example / README.md   # configuration template and startup instructions
```

## 4. API Endpoints (implemented)

**All routes below are implemented** in `internal/handler` with Swagger annotations
(spec in `docs/`, UI at `/swagger/*`). Authorization rules (ownership, role checks,
membership) are enforced in the **service layer**; handlers never trust client-supplied
user IDs — the actor is loaded from the validated access token. Errors use a consistent
JSON envelope `{"error":{"code","message"},"request_id"}` with static, secret-safe messages.
The expensive auth operations (register/login/refresh/change-password) are rate-limited
per client IP (HTTP 429 + `Retry-After`). User/project/task PATCH updates use optimistic
concurrency: a concurrently modified record returns 409 `conflict`; a deleted/missing
record returns 404.

### Authentication

- `POST /api/v1/auth/register` — create account (name, email, password); store hash only.
- `POST /api/v1/auth/login` — verify credentials; issue access token + refresh token with a fresh `session_id`.
- `POST /api/v1/auth/refresh` — rotate refresh token (row-locked, reuse-detecting; workflow in §7).
- `POST /api/v1/auth/logout` — revoke the current session's active refresh token.
- `POST /api/v1/auth/logout-all` — revoke all active refresh sessions of the user.

### Users

- `GET /api/v1/users/me` — current user profile.
- `PATCH /api/v1/users/me` — update name/email (email normalized; `lower(email)` uniqueness applies).
- `PATCH /api/v1/users/me/password` — change password after verifying the current one; revokes ALL refresh sessions atomically (implemented safe default).
- `DELETE /api/v1/users/me` — hard delete, subject to the RESTRICT chain (§6); returns 409 while created projects/tasks or memberships exist.

### Projects

- `POST /api/v1/projects` — create project + owner membership in one transaction.
- `GET /api/v1/projects` — list projects where the caller is a member.
- `GET /api/v1/projects/:id` — project detail; members only.
- `PATCH /api/v1/projects/:id` — update name/description; owner/admin.
- `DELETE /api/v1/projects/:id` — delete project; cascades memberships and tasks; owner only.

### Project members

- `GET /api/v1/projects/:id/members` — list members and roles.
- `POST /api/v1/projects/:id/members` — add a member; owner/admin.
- `PATCH /api/v1/projects/:id/members/:userId` — change role between admin/member; owner role changes only via transfer.
- `DELETE /api/v1/projects/:id/members/:userId` — remove member; 409 while assigned tasks exist; owner removal requires prior transfer.
- `POST /api/v1/projects/:id/transfer-ownership` — transactional owner transfer (project row lock, demote before promote).

### Tasks

- `POST /api/v1/projects/:id/tasks` — create task; any project member may create (confirmed rule); assignee must be a member of the same project.
- `GET /api/v1/projects/:id/tasks` — list with **pagination** (limit/offset or cursor), **filtering** (status, priority, assigned_to, due-date range), and **sorting** (created_at, due_at, priority, status).
- `GET /api/v1/projects/:id/tasks/:taskId` — task detail.
- `PATCH /api/v1/projects/:id/tasks/:taskId` — update title/description/status/priority/due_at/assignment (composite FK enforces same-project membership).
- `DELETE /api/v1/projects/:id/tasks/:taskId` — delete task.
- `GET /api/v1/users/me/tasks` — cross-project "my assigned tasks" (served by the partial assignee index); same pagination/filtering/sorting.

### Health and documentation (implemented)

- `GET /health` — liveness probe.
- `GET /ready` — readiness probe; pings the pool with a 3s timeout (registered by `cmd/api`).
- `GET /swagger/*` — Swagger UI serving the generated spec (swaggo/echo-swagger, approved).

Task write policy (approved): update/delete allowed for the task **creator, current
assignee, or project owner/admin**; all members may create/list/view. Task listing
supports `limit` (1–200, default 50), `offset`, filters (`status`, `priority`,
`assigned_to`, `due_after`, `due_before`), and whitelisted sorting (`sort_by` =
created_at|due_at|priority|status, `sort_order` = asc|desc). PATCH null-semantics:
`assigned_to:null` unassigns, `due_at:null` clears, project `description:null` clears;
task `description:null` is rejected (service patch cannot NULL it — known limitation).

## 5. Finalized database schema (PostgreSQL 17)

Five tables. The DDL below is **verbatim from the `migrations/000001`–`000005` up files**
(authoritative; constraint and index names aligned with the actual SQL).
**Applied to the database** (`schema_migrations` version=5, dirty=false; verified read-only).
Whitespace checks use `~ '[^[:space:]]'` (rejects whitespace-only incl. tabs/newlines).

```sql
-- migrations/000001_create_users_table.up.sql
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    name VARCHAR(100) NOT NULL
        CHECK (name ~ '[^[:space:]]'),

    email TEXT NOT NULL
        CHECK (char_length(email) <= 254),

    password_hash TEXT NOT NULL
        CHECK (password_hash <> ''),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_lower_uidx
    ON users (lower(email));

-- migrations/000002_create_refresh_sessions_table.up.sql
-- One row per refresh token; session_id stable across the whole rotation chain.
CREATE TABLE refresh_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    session_id UUID NOT NULL,

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    token_hash BYTEA NOT NULL UNIQUE
        CHECK (octet_length(token_hash) = 32),

    expires_at TIMESTAMPTZ NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    revoked_at TIMESTAMPTZ NULL,

    replaced_by UUID NULL,

    CONSTRAINT refresh_sessions_expires_after_created
        CHECK (expires_at > created_at),

    CONSTRAINT refresh_sessions_replaced_requires_revocation
        CHECK (replaced_by IS NULL OR revoked_at IS NOT NULL),

    CONSTRAINT refresh_sessions_no_self_replacement
        CHECK (replaced_by IS NULL OR replaced_by <> id),

    CONSTRAINT refresh_sessions_replaced_by_fkey
        FOREIGN KEY (replaced_by)
        REFERENCES refresh_sessions(id)
        ON DELETE SET NULL
        DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX refresh_sessions_one_active_per_session_uidx
    ON refresh_sessions (session_id)
    WHERE revoked_at IS NULL;

CREATE INDEX refresh_sessions_user_id_idx
    ON refresh_sessions (user_id);

CREATE INDEX refresh_sessions_session_id_idx
    ON refresh_sessions (session_id);

CREATE INDEX refresh_sessions_revoked_at_idx
    ON refresh_sessions (revoked_at)
    WHERE revoked_at IS NOT NULL;

CREATE INDEX refresh_sessions_expires_at_idx
    ON refresh_sessions (expires_at);

-- migrations/000003_create_projects_table.up.sql
CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    name VARCHAR(150) NOT NULL
        CHECK (name ~ '[^[:space:]]'),

    description TEXT NULL,

    created_by UUID NOT NULL
        REFERENCES users(id) ON DELETE RESTRICT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX projects_created_by_idx
    ON projects (created_by);

-- migrations/000004_create_project_members_table.up.sql
CREATE TABLE project_members (
    project_id UUID NOT NULL
        REFERENCES projects(id) ON DELETE CASCADE,

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE RESTRICT,

    role TEXT NOT NULL
        CHECK (role IN ('owner', 'admin', 'member')),

    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (project_id, user_id)
);

CREATE UNIQUE INDEX project_members_one_owner_per_project_uidx
    ON project_members (project_id)
    WHERE role = 'owner';

CREATE INDEX project_members_user_id_idx
    ON project_members (user_id);

-- migrations/000005_create_tasks_table.up.sql
CREATE TABLE tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    project_id UUID NOT NULL
        REFERENCES projects(id) ON DELETE CASCADE,

    created_by UUID NOT NULL
        REFERENCES users(id) ON DELETE RESTRICT,

    assigned_to UUID NULL,

    title VARCHAR(200) NOT NULL
        CHECK (title ~ '[^[:space:]]'),

    description TEXT NULL,

    status TEXT NOT NULL DEFAULT 'todo'
        CHECK (status IN ('todo', 'in_progress', 'done')),

    priority TEXT NOT NULL DEFAULT 'medium'
        CHECK (priority IN ('low', 'medium', 'high', 'urgent')),

    due_at TIMESTAMPTZ NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT tasks_project_assignee_fkey
        FOREIGN KEY (project_id, assigned_to)
        REFERENCES project_members (project_id, user_id)
        MATCH SIMPLE
        ON DELETE NO ACTION
);

CREATE INDEX tasks_project_assignee_idx
    ON tasks (project_id, assigned_to);

CREATE INDEX tasks_assignee_idx
    ON tasks (assigned_to)
    WHERE assigned_to IS NOT NULL;

CREATE INDEX tasks_creator_idx
    ON tasks (created_by);
```

Relationships: users 1—N refresh_sessions (cascade); users 1—N projects via `created_by`
(restrict); projects 1—N project_members (cascade) and 1—N tasks (cascade); users 1—N
project_members (restrict); tasks N—1 users via `created_by` (restrict, historical
attribution survives member removal); tasks assignee → project_members via **composite FK**
`(project_id, assigned_to)`; refresh_sessions self-reference via `replaced_by` (rotation chain).

## 6. Constraints, indexes, transaction rules, deletion behavior

### FK deletion policies (final)

| FK | Action | Notes |
|---|---|---|
| refresh_sessions.user_id → users | CASCADE | |
| refresh_sessions.replaced_by → refresh_sessions.id | SET NULL, **DEFERRABLE INITIALLY DEFERRED** | deferral applies to the FK *check* only; the SET NULL *action* fires at end of the DELETE statement (non-deferrable AFTER trigger) |
| projects.created_by → users | RESTRICT | |
| project_members.project_id → projects | CASCADE | |
| project_members.user_id → users | RESTRICT | confirmed decision |
| tasks.project_id → projects | CASCADE | |
| tasks.created_by → users | RESTRICT | direct FK, not composite (decision) |
| tasks.(project_id, assigned_to) → project_members | **NO ACTION** (not RESTRICT), MATCH SIMPLE | NULL assignee skips check; NO ACTION queues its check behind cascade events so project deletion with assigned tasks succeeds; standalone member removal with assigned tasks still fails (23503) |

### Key rules

- Partial unique indexes: one active refresh token per session (`WHERE revoked_at IS NULL` —
  `now()` cannot appear in index predicates); at most one owner per project (`WHERE role = 'owner'`).
- `token_hash` uniqueness is **global** (revoked rows must remain findable for reuse detection).
- Index rationale: `tasks_project_assignee_idx` replaces a standalone `(project_id)` index
  (leading column covers project-only scans/cascades; exact probe for the composite FK check;
  serves project+assignee and project+unassigned queries). Partial assignee-only index retained
  for cross-project "my tasks". All referencing FK columns are indexed.
- **App-enforced invariants** (no DB triggers, by decision): ≥1 owner per project;
  same-user/same-session `replaced_by` links; project creation + owner membership atomicity.
  All membership mutations must go through the service that takes a
  `SELECT ... FROM projects WHERE id = $1 FOR UPDATE` row lock.
- Owner transfer: one transaction, project row lock, **demote before promote**.
- Member removal: unassign/reassign assigned tasks first; owner must transfer before removal.
- User deletion (RESTRICT chain): unassign/remove assigned tasks → delete created tasks or
  their projects → remove memberships → delete/transfer created projects → delete user
  (cascades refresh_sessions). Content creators effectively cannot be hard-deleted while
  their content exists (accepted consequence).
- Isolation: READ COMMITTED + row locks; retry on `40P01` / `40001`; map `23503` → HTTP 409.
- **Optimistic concurrency (F-02):** user/project/task `UPDATE`s carry
  `WHERE id = $1 AND updated_at = <value originally read>`. A zero-row result is
  disambiguated by an existence probe: still present → `ErrConflict` (HTTP 409,
  concurrently modified); gone → `ErrNotFound` (HTTP 404). `updated_at` always advances
  strictly via `GREATEST(now(), updated_at + interval '1 microsecond')`, so successive
  updates never collide even within one clock tick. No schema change/migration required
  (`updated_at` already exists). **Partial-field merging lives in the service (R-01):**
  `UserService.UpdateProfile(name, email *string)` and `ProjectService.Update(ProjectPatch)`
  merge only the supplied fields onto their freshest read, so an omitted field keeps its
  latest committed value (handlers no longer pre-read and merge, which previously left a
  lost-update window). Task PATCH already used service-side `TaskPatch` merging.
  Authorization and validation are unchanged. The non-PATCH writers `UpdatePasswordHash`
  and `UnassignMemberTasks` also use the `GREATEST` advance (R-03) so the version never
  regresses; they carry no version predicate (they do not rewrite PATCH-managed fields).
- `updated_at` is app-managed (no triggers) and doubles as the optimistic-concurrency
  version. `due_at` has no cross-field constraint.

## 7. Authentication, rotation, sessions, security

- Passwords: hash only (`password_hash`); **argon2id finalized** (approved): t=1, m=64MiB,
  p=4, 32-byte key, 16-byte salt, PHC string format, constant-time comparison; policy
  8–128 characters. Plaintext never stored, logged, or returned.
- Access tokens (approved): HS256 JWT, `typ=access` claim, `sub` = user UUID, expiry
  required, 15-minute default TTL; signing secret `AUTH_ACCESS_SECRET` ≥ 32 bytes,
  injected via constructor, never logged. Refresh tokens are a distinct opaque type and
  are always rejected by access-token validation. Stateless access tokens remain valid
  until expiry after logout (≤15m window; no denylist — accepted trade-off).
- **Rate limiting (F-01/R-02):** register/login/refresh/change-password are guarded by an
  in-process, per-IP token bucket (`internal/handler/ratelimit.go`): burst 5, sustained
  one request per `AUTH_RATE_LIMIT_INTERVAL` (default 1s). **All four endpoints share one
  bucket per IP** (a login burst can consume tokens that also throttle refresh from the
  same IP — intentional conservative default). It keys on `c.RealIP()` with Echo's default
  nil extractor (connection peer address) — forwarding headers are neither trusted nor
  parsed, so they cannot spoof or evade the limit. Memory is bounded (10k entries) with
  **bounded-cost sampled eviction** (inspect ≤16 entries, evict a stale one if sampled else
  the oldest sampled), so a high-cardinality IP flood cannot amplify CPU on the request
  path. Denied requests get HTTP 429, code `rate_limited`, and a `Retry-After` header.
  **Limits are per process instance**: multi-replica deployments must also enforce limits
  at a reverse proxy / shared gateway. Concurrency-safe (mutex).
- Email: stored as `text`, uniqueness via `lower(email)` expression index; service normalizes
  to lowercase and queries with `WHERE lower(email) = lower($1)`.
- Refresh tokens: 256-bit random from `crypto/rand`; stored as SHA-256 `bytea(32)`, globally
  unique. SHA-256 is acceptable here because tokens are high-entropy (unlike passwords).
- Session model: one row per token; fresh `session_id` per login (multi-device = multiple
  sessions); rotation keeps the same `session_id`.
- **Rotation workflow** (service, READ COMMITTED):
  1. `SELECT ... WHERE token_hash = $1 FOR UPDATE`.
  2. Not found → 401. `revoked_at` set → **reuse detected**: revoke all active rows of that
     `session_id`, commit, 401, force re-login. Expired → mark revoked, 401.
  3. Valid → `UPDATE` old row (`revoked_at = now()`, `replaced_by = $new_id`) **then** `INSERT`
     new row (same session/user) → COMMIT validates the deferred FK. Order matters: the
     deferral allows revoke-before-insert without violating the partial unique index.
     Never `SET CONSTRAINTS ... IMMEDIATE` between the two statements.
  4. Concurrent presentations serialize on the row lock; the loser takes the reuse path.
- Logout/session revocation: `UPDATE ... SET revoked_at = now() WHERE session_id = $1 AND
  revoked_at IS NULL` (`replaced_by` stays NULL — revocation, not rotation).
- **Cleanup** (scheduled Go job, not pg_cron). PostgreSQL `DELETE` does **not** support a
  direct `LIMIT` clause; batch deletion uses a CTE that selects up to 500 eligible ids with
  deterministic ordering, then `DELETE ... USING`:

  ```sql
  WITH batch AS (
      SELECT id FROM refresh_sessions
      WHERE (revoked_at IS NOT NULL AND revoked_at < now() - interval '30 days')
         OR (revoked_at IS NULL     AND expires_at < now() - interval '30 days')
      ORDER BY COALESCE(revoked_at, expires_at), id
      LIMIT 500
  )
  DELETE FROM refresh_sessions rs
  USING batch b
  WHERE rs.id = b.id;
  ```

  Both retention conditions are preserved: revoked records older than 30 days, and
  never-revoked expired records whose expiry is older than 30 days. The Go job repeats
  batches (each its own transaction) until a batch returns fewer rows than the
  configured batch limit (500). Run off-peak;
  optional `pg_try_advisory_lock` prevents overlapping runs. SET NULL keeps cleanup
  reference-safe without guards or ordering — the action fires during the DELETE statement
  (see the FK table in §6), so no commit-time surprises. Concurrency: cleanup's SET NULL
  updates take row locks on retained revoked rows and can briefly contend with rotation's
  `FOR UPDATE` on the same row — keep batches small and retain `40P01`/`40001` retries on
  both paths. Reuse-detection window equals retention; chain links older than 30 days may
  be nulled (partial audit trail).
- DB is bound to localhost only; credentials live in gitignored `.env` (see §9).

## 8. Conventions and testing strategy

Conventions (confirmed): SQL-first repositories, no ORM; manual DI; layered calls
(handler → service → repository); services own transactions/locking; `text` + CHECK over
enums; `timestamptz`; trim/normalize inputs app-side (DB rejects whitespace-only via regex
CHECKs; `varchar` limits: name 100, project name 150, title 200); golang-migrate migrations
are implicitly transactional — no `CREATE INDEX CONCURRENTLY` inside them.

Testing: table-driven Go unit tests are **implemented and fully offline** (config, pool
failure paths, domain validation, repository query building/error mapping, service
policies/rotation/rollback/retries via in-memory fakes, real argon2id + JWT suites, HTTP
handler/middleware tests with stub services). Integration tests T1–T7 are **implemented**
in `internal/integration` under the `integration` build tag: Testcontainers
(`testcontainers-go` v0.45.0 + postgres module) starts an isolated temporary
`postgres:17-alpine` container, applies the embedded migrations through the real
`migration.Run`, and terminates it afterwards (Ryuk reaps on abnormal exit). The suite
never touches the development database/container/volume and never reads `.env`
(test-only dummy credentials). Commands:

```sh
go test -count=1 ./...                                          # unit only, no Docker
go test -count=1 -tags=integration ./internal/integration/...   # T1–T7, Docker required
```

CI (`.github/workflows/ci.yml`, push to main + PRs): gofmt check, `go vet`, `go build`,
unit tests (no Docker), plus a separate integration job running the tagged suite on the
GitHub-hosted runner's Docker. Go version comes from `go.mod`; no secrets required.

The four required scenarios (all covered; RI trigger order depends on constraint
creation order, hence the real migration-built schema):

1. **T1** — delete a project with assigned tasks (multiple members, incl. assignee=owner)
   → succeeds; memberships and tasks gone; users untouched.
2. **T2** — remove a member with assigned tasks → fails `23503`
   (`tasks_project_assignee_fkey`); after unassign/reassign → succeeds; removing a member who
   only *created* tasks succeeds (attribution preserved); owner removal requires transfer.
3. **T3** — N≈16 concurrent rotations of the same raw token via the real service → exactly
   one success, others 401 via reuse path; final state: zero active rows for the session;
   `R1.replaced_by = R2.id`; no unretried deadlocks; variants: two sessions of one user rotate
   independently; expired/unknown tokens → 401; chain replay revokes session.
4. **T4** — cleanup of old chains: revoked 40d/35d deleted, revoked 5d/10d and active kept;
   expired-never-revoked 40d deleted; boundary ±1min around 30d cutoff; batch size 1 and ≥10;
   SET NULL observable within the deleting transaction; idempotent second run; concurrent
   cleanup + rotation race stays consistent.

Coverage map: T1 → `t1_project_delete_test.go`; T2 → `t2_member_removal_test.go`
(incl. raw 23503 constraint-name assertion, reassign/unassign variants, creator
attribution, owner transfer); T3 → `t3_rotation_test.go` (16-goroutine single-winner,
independent sessions, expired/unknown, chain replay); T4 → `t4_cleanup_test.go`
(boundaries in the PG clock domain via SQL intervals — no clock-skew flakiness);
T5 → `t5_optimistic_concurrency_test.go` (stale-write conflict, deleted-row not-found,
concurrent different-field updates without lost updates, strict `updated_at` advance
incl. the `GREATEST` future-stamp branch); T6 → `t6_partial_merge_test.go` (R-01: partial
user/project updates preserve the latest committed omitted field); T7 →
`t7_timestamp_advance_test.go` (R-03: `UpdatePasswordHash`/`UnassignMemberTasks` advance
`updated_at` past a future stamp). F-01 rate limiting is covered by offline
limiter unit tests (burst/deny/recover with an injected clock, 200-goroutine concurrency,
bounded-cost sampled eviction + capacity under a 500-IP concurrent flood) and handler
tests (429 + `Retry-After`, spoofed `X-Forwarded-For`/`X-Real-IP` ignored, per-peer buckets,
recovery, service short-circuit, refresh/change-password coverage). F-02/R-01 are
additionally covered by service fake tests (conflict-then-retry, not-found,
preserve-latest-omitted-field) and handler tests (409/404 mapping for all three resources;
regression assertions that user/project PATCH no longer pre-reads to merge omitted fields).

## 9. Current implementation status

**Implemented and verified (gofmt / `go test -count=1 ./...` / `go vet` / `go build` all green):**

- `go.mod`/`go.sum`: module `github.com/metamin44/go-task-management-api`; direct deps:
  Echo v4.16.5, pgx/v5 v5.13.0, golang-migrate v4.20.1, godotenv v1.5.1,
  golang-jwt/v5 v5.3.1, swaggo/echo-swagger v1.5.2, swaggo/swag v1.16.6,
  testcontainers-go v0.45.0 + modules/postgres v0.45.0 (integration tests only)
  (+ transitive deps incl. pgerrcode, x/crypto for argon2id, swaggo files/v2,
  go-openapi, x/tools, docker/moby stack, klauspost/compress v1.20.4 — pinned
  above v1.18.7 whose proxy zip was unavailable). Some older entries still carry
  cosmetic `// indirect` markers (`go mod tidy` has deliberately not been run).
- `internal/config`: `Load()` with env-over-`.env` precedence, full validation,
  secret-safe errors, `DBConfig.DSN()`; **AuthConfig**: `AUTH_ACCESS_SECRET`
  (required, ≥32 bytes, preserved exactly), `AUTH_ACCESS_TTL` (default 15m),
  `AUTH_REFRESH_TTL` (default 720h), `AUTH_RATE_LIMIT_INTERVAL` (default 1s). No
  `String()`/logging helpers; unit-tested.
- `internal/database`: `NewPool(ctx, cfg)` — pgxpool bootstrap with Ping check and
  secret-safe errors; unit-tested (no live DB required).
- `internal/migration`: `Run(cfg, migrationsFS)` — iofs + migrate `pgx5` driver,
  `ErrNoChange` = success, static secret-safe errors.
- `migrations/`: five up+down pairs (000001–000005) + `embed.go`; **applied**
  (version=5, dirty=false; tables/FKs/indexes verified read-only).
- `internal/domain`: entities, enums, validation (DB-identical limits), UUID
  ([16]byte, pgx-native), error sentinels (incl. `ErrSerialization`), repository
  interfaces + `Transactor`; unit-tested.
- `internal/repository`: five SQL-first pgx repositories, ctx-carried transactions,
  constraint-aware error mapping (23505→conflict, 23503→reference, 23514→invalid,
  40P01/40001→serialization), whitelisted task-list query builder, batched cleanup
  CTE, optimistic-concurrency `updated_at` predicate on user/project/task updates
  (conflict vs not-found disambiguation, strict `GREATEST` advance); offline unit tests.
- `internal/service`: auth (register/login/refresh rotation with committed
  reuse-revocation/logout/logout-all/password change), users, projects (atomic
  project+owner create), members (row-locked mutations, owner protection, atomic
  transfer, guarded removal, list), tasks (approved write policy); argon2id hasher;
  HS256 JWT manager; bounded 3-attempt serialization retries; ~90 offline tests
  incl. failure injection and rollback.
- `internal/handler`: all §4 endpoints; strict JSON DTOs (unknown fields rejected,
  1 MiB body cap), consistent error envelope, request ID, metadata-only structured
  logging, panic recovery (no stack/value leakage), JWT auth middleware loading
  current identity, conservative CORS whitelist (disabled by default), per-IP auth
  rate limiter (429 + Retry-After, bounded memory); Swagger
  annotations; offline HTTP test functions (incl. rate-limit and 409/404 mapping).
- `cmd/api`: composition root — config → slog JSON logger → pool → repositories →
  transactor → services → JWT → handler → Echo; server timeouts from config;
  `/ready` pings the pool (3s bound); `/swagger/*` UI; SIGINT/SIGTERM graceful
  shutdown (10s bound) then pool close; migrations are a separate explicit step.
- `cmd/migrate`: applies embedded migrations; secret-safe stderr errors.
- `docs/`: generated OpenAPI spec (`swag init -g internal/handler/doc.go -o docs`).
- `internal/integration/`: T1–T7 live-PostgreSQL suite (build tag `integration`),
  Testcontainers harness with real migration runner, per-test TRUNCATE isolation,
  deterministic concurrency tests (barriers, no timing assertions); all 18 tests
  pass locally against an isolated container.
- `.github/workflows/ci.yml`: push-to-main + PR pipeline (gofmt, vet, build, unit;
  separate integration job on runner Docker; Go version from go.mod; no secrets).
- `Dockerfile` (multi-stage, non-root uid 10001, wget healthcheck, `.env` excluded
  via `.dockerignore`), `docker-compose.yml` (migrate one-shot → api; **no db
  service/volumes/networks** — the existing container is preserved), `.env.example`
  (incl. AUTH_* placeholders), `README.md`, this document.

**NOT implemented / NOT done:**

- Docker image has not been built/run yet (files only; no Docker operations were
  performed against the development database).
- CORS origins are unconfigured (same-origin only) — deliberate conservative default.

Environment (running, verified):

| Item | Value |
|---|---|
| Container | `task-management-db` |
| Image / server | `postgres:17-alpine` / **PostgreSQL 17.12** |
| Database | `task_management` |
| Username | `app_user` |
| Port binding | **`127.0.0.1:5432 → 5432` (localhost only)** |
| Named volume | `task-management-postgres-data` → `/var/lib/postgresql/data` |
| Restart policy | `unless-stopped` |
| Password | stored in `.env` as `POSTGRES_PASSWORD` (gitignored). **Never read, print, copy, or commit its value.** Pass via env-var passthrough only. |
| Cluster fingerprint | `system_identifier = 7709552659860402212` (data preserved across container recreate) |

Connection (for future code): `host=127.0.0.1 port=5432 dbname=task_management user=app_user
sslmode=disable`, password loaded from `.env` at runtime.

## 10. Next implementation steps

Completed: module + dependencies; config (incl. AUTH_*); connection pool; migration
runner; migration files 000001–000005 (up+down) **applied**; domain + repositories
(Phase 1); services + security policies (Phase 2); HTTP handlers + middleware + Swagger
annotations (Phase 3); composition root, auth config wiring, Swagger UI, Docker/Compose
files, README (Phase 4); router separation; **integration tests T1–T7 + GitHub Actions
CI**; scheduled refresh-session cleanup worker and login timing equalization (§12).
Remaining, in order:

1. Optional deployment hardening: explicit CORS whitelist per environment, production
   `DB_SSLMODE=verify-full` with CA certs, secret rotation procedure for
   `AUTH_ACCESS_SECRET`, image build/publish pipeline, and reverse-proxy/gateway rate
   limiting for multi-replica deployments (in-process per-IP auth limiting is already
   implemented but is per-instance only).

Open items: hard-delete semantics for content creators (RESTRICT by design — revisit
only if account-erasure requirements appear); task `description` cannot be set to NULL
via PATCH (service `TaskPatch` shape — extend only with approval).
Resolved since earlier drafts: password hashing = argon2id (approved parameters, §7);
access tokens = HS256 JWT 15m / refresh 30d (approved); task write policy =
creator/assignee/owner/admin (approved); `joined_at NOT NULL DEFAULT now()` is fixed
in the SQL.

## 11. Post-audit remediation batch (completed)

- `.gitignore`: now ignores `.env` and `.env.*` while keeping `.env.example`
  trackable (`!.env.example`); all prior rules preserved.
- Stale "T1–T4" labels corrected to "T1–T7" in `.github/workflows/ci.yml`
  (job name only — workflow behavior unchanged), `README.md`, and this file.
- Q-01 tests added (no production behavior change):
  `TestUpdateProfileExplicitNullFields` (handler — explicit JSON null on
  name/email is treated as omitted per current semantics; null-only bodies
  still 400); `TestUpdateProfileConcurrentDifferentFieldsNoLostUpdate` and
  `TestProjectUpdateConcurrentDifferentFieldsNoLostUpdate` (service — a stale
  different-field update conflicts, and a fresh retry preserves the concurrent
  writer's committed value).
- Q-02 tests strengthened/added: `TestRateLimiterCapacityUnderConcurrency` now
  asserts exact saturation (`len == max`) in addition to the upper bound;
  `TestRateLimiterSampledEvictionOldestWhenNoneStale` and
  `TestRateLimiterSampledEvictionPrefersStale` verify eviction deterministically
  at capacity ≤ sample size (16) using the injected clock — no limiter design
  change.
- Verification: `gofmt -l .` clean; `go test -count=1 ./...` all packages pass
  (6 new/strengthened tests confirmed PASS); `go vet ./...`, `go build ./...`,
  `go vet -tags=integration ./...` all clean. Integration suite not re-run this
  batch (no Docker per constraints); last full run 18/18 T1–T7 green.
  staticcheck remains unusable locally (built with go1.24.3 vs module go1.27.3);
  `-race` unavailable (no cgo/gcc). No commits/pushes; `.env` never read.

## 12. M-01 cleanup worker + L-01 login timing equalization (completed)

- **M-01 — scheduled refresh-session cleanup.** `internal/service/cleanup.go` adds
  `CleanupWorker`, reusing the existing `DeleteExpiredBatch` CTE (no SQL duplication,
  no semantics change). Policy preserved from §6: 30-day retention, 500-row batches,
  each batch its own transaction, drained until a batch returns fewer than the limit.
  A per-tick bound (`cleanupMaxBatches = 200`) caps work so a backlog cannot monopolize
  the worker/pool; the remainder is handled next tick. `Run` is a single-goroutine
  ticker loop → passes never overlap within an instance; it checks `ctx` before each
  batch and returns promptly on cancellation. Errors are logged secret-safe
  (`slog.Warn`) and the worker waits for the next tick (no tight retry). Config:
  `AUTH_CLEANUP_INTERVAL` (default 1h, validated > 0). Wired in `cmd/api/main.go` with
  a dedicated cancellable context; LIFO defers cancel → join the worker before
  `pool.Close`, so shutdown never deadlocks (including the early server-error path).
  Multi-replica coordination (advisory lock / single runner) remains optional future
  work, consistent with the per-instance rate-limit note.
- **L-01 — login timing equalization.** `AuthService.Login` now calls `verifyDummy`
  on the unknown-email path: one Argon2id `Verify` against a dummy PHC hash generated
  exactly once via `sync.Once` (never per login). Both unknown-email and wrong-password
  paths perform one verification and return the same `ErrUnauthorized` → identical 401.
  No password/hash/token is logged. If dummy generation fails (entropy error),
  verification is skipped but the response stays `ErrUnauthorized`.
- **Tests (offline, no Docker):** `TestLoginTimingEqualization` (counting hasher proves
  both failure paths do exactly one Verify; dummy generated once, reused thereafter),
  `TestLoginUnknownUserDummyHashIsValidArgon2id` (real hasher; dummy is valid PHC, Verify
  returns false without format error). Cleanup: `TestCleanupWorkerDrainsUntilExhausted`
  (policy 30d/500 preserved), `...BoundedPerTick`, `...ErrorLogsAndStops`,
  `...RunCancellation`, `...CancellationMidPass`, `...NoOverlap` (peak concurrency == 1),
  `...Defaults`. All 9 new tests PASS.
- **Verification:** `gofmt -l .` clean; `go test -count=1 ./...` all packages pass;
  `go vet ./...`, `go build ./...`, `go vet -tags=integration ./...` clean. Integration
  suite not re-run (no Docker per constraints); last full run 18/18 T1–T7 green.
  staticcheck still unusable locally (go1.24.3 vs go1.27.3); `-race` unavailable (no
  cgo/gcc). No schema/migration changes; no new dependencies; no commits/pushes;
  `.env` never read.