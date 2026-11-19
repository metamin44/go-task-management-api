//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/metamin44/go-task-management-api/internal/config"
	"github.com/metamin44/go-task-management-api/internal/database"
	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/migration"
	"github.com/metamin44/go-task-management-api/internal/repository"
	"github.com/metamin44/go-task-management-api/internal/service"
	"github.com/metamin44/go-task-management-api/migrations"
)

// Test-only dummy credentials for the isolated container. They are not
// secrets, never come from .env, and are never used against any real database.
const (
	testImage      = "postgres:17-alpine"
	testDBName     = "integration_test"
	testDBUser     = "integration"
	testDBPassword = "integration-test-only"
	testJWTSecret  = "integration-test-signing-secret-0123456789"
)

var testPool *pgxpool.Pool

// TestMain starts ONE isolated PostgreSQL container for the whole package,
// applies the embedded migrations via the real runner, and guarantees
// termination even when tests fail. Each test truncates all tables first
// (newFixture) for deterministic isolation without per-test containers.
func TestMain(m *testing.M) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, testImage,
		postgres.WithDatabase(testDBName),
		postgres.WithUsername(testDBUser),
		postgres.WithPassword(testDBPassword),
		// Readiness: log "ready to accept connections" twice (initdb restart)
		// then the mapped port actually serving — required on Docker Desktop.
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: failed to start isolated PostgreSQL container (Docker required)")
		os.Exit(1)
	}

	code := runSuite(ctx, pg, m)

	if err := pg.Terminate(context.Background()); err != nil {
		// Ryuk reaps the container even if explicit termination fails.
		fmt.Fprintln(os.Stderr, "integration: explicit container termination failed; reaper will clean up")
	}
	os.Exit(code)
}

func runSuite(ctx context.Context, pg *postgres.PostgresContainer, m *testing.M) int {
	host, err := pg.Host(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: cannot resolve container host")
		return 1
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: cannot resolve container port")
		return 1
	}
	cfg := config.DBConfig{
		Host: host, Port: int(port.Num()),
		Name: testDBName, User: testDBUser, Password: testDBPassword,
		SSLMode: "disable", MaxConns: 10, MinConns: 0, MaxConnLifetime: 30 * time.Minute,
	}
	// Real migration path: embedded SQL through the production runner.
	if err := migration.Run(cfg, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, "integration: applying embedded migrations failed:", err)
		return 1
	}
	pool, err := database.NewPool(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: pool bootstrap failed")
		return 1
	}
	defer pool.Close()
	testPool = pool
	return m.Run()
}

// fixture bundles real repositories and services over the shared pool.
type fixture struct {
	pool       *pgxpool.Pool
	users      *repository.UserRepository
	sessions   *repository.RefreshSessionRepository
	projects   *repository.ProjectRepository
	members    *repository.ProjectMemberRepository
	tasks      *repository.TaskRepository
	tx         *repository.Transactor
	auth       *service.AuthService
	userSvc    *service.UserService
	projectSvc *service.ProjectService
	memberSvc  *service.MemberService
	taskSvc    *service.TaskService
}

// newFixture truncates all tables (CASCADE) for a clean, isolated state and
// wires the full real stack — the same composition as cmd/api.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx,
		"TRUNCATE TABLE users, refresh_sessions, projects, project_members, tasks CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f := &fixture{
		pool:     testPool,
		users:    repository.NewUserRepository(testPool),
		sessions: repository.NewRefreshSessionRepository(testPool),
		projects: repository.NewProjectRepository(testPool),
		members:  repository.NewProjectMemberRepository(testPool),
		tasks:    repository.NewTaskRepository(testPool),
		tx:       repository.NewTransactor(testPool),
	}
	tokens, err := service.NewJWTAccessTokens([]byte(testJWTSecret), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.auth, err = service.NewAuthService(f.users, f.sessions, f.tx, service.NewArgon2idHasher(), tokens, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.userSvc = service.NewUserService(f.users)
	f.projectSvc = service.NewProjectService(f.projects, f.members, f.tx)
	f.memberSvc = service.NewMemberService(f.projects, f.members, f.users, f.tasks, f.tx)
	f.taskSvc = service.NewTaskService(f.tasks, f.members)
	return f
}

// ---- helpers ----

func tokenHash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func seedUser(t *testing.T, f *fixture, email string) domain.UUID {
	t.Helper()
	u := &domain.User{ID: domain.NewUUID(), Name: "Test User", Email: email, PasswordHash: "integration-test-hash"}
	if err := f.users.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u.ID
}

// loginPair registers via the real argon2id path and logs in, returning a
// genuine service-issued token pair.
func loginPair(t *testing.T, f *fixture, email string) (*service.TokenPair, domain.UUID) {
	t.Helper()
	ctx := context.Background()
	u, err := f.auth.Register(ctx, "Test User", email, "password123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	pair, err := f.auth.Login(ctx, email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return pair, u.ID
}

func countRows(t *testing.T, f *fixture, table string) int64 {
	t.Helper()
	var n int64
	// table is always a compile-time constant from the fixed schema list.
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func countActive(t *testing.T, f *fixture, sessionID domain.UUID) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM refresh_sessions WHERE session_id = $1 AND revoked_at IS NULL",
		sessionID).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

func sessionIDByHash(t *testing.T, f *fixture, raw string) domain.UUID {
	t.Helper()
	var sid domain.UUID
	if err := f.pool.QueryRow(context.Background(),
		"SELECT session_id FROM refresh_sessions WHERE token_hash = $1", tokenHash(raw)).Scan(&sid); err != nil {
		t.Fatalf("session by hash: %v", err)
	}
	return sid
}
