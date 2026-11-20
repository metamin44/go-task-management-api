package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// Focused HTTP tests: stub services (no database), real Echo routing and
// middleware. Business policy itself is covered by the service-layer tests;
// here we verify transport behavior: status codes, envelopes, validation,
// authentication, and leakage safety.

var errUnexpected = errors.New("unexpected service call")

var testUID = domain.NewUUID()

const testToken = "valid-test-token"

type stubAuth struct {
	registerFn       func(context.Context, string, string, string) (*domain.User, error)
	loginFn          func(context.Context, string, string) (*service.TokenPair, error)
	refreshFn        func(context.Context, string) (*service.TokenPair, error)
	logoutFn         func(context.Context, string) error
	logoutAllFn      func(context.Context, domain.UUID) (int64, error)
	changePasswordFn func(context.Context, domain.UUID, string, string) error
	validateFn       func(string, time.Time) (*service.TokenClaims, error)
}

func (s *stubAuth) Register(ctx context.Context, name, email, password string) (*domain.User, error) {
	if s.registerFn != nil {
		return s.registerFn(ctx, name, email, password)
	}
	return nil, errUnexpected
}

func (s *stubAuth) Login(ctx context.Context, email, password string) (*service.TokenPair, error) {
	if s.loginFn != nil {
		return s.loginFn(ctx, email, password)
	}
	return nil, errUnexpected
}

func (s *stubAuth) Refresh(ctx context.Context, token string) (*service.TokenPair, error) {
	if s.refreshFn != nil {
		return s.refreshFn(ctx, token)
	}
	return nil, errUnexpected
}

func (s *stubAuth) Logout(ctx context.Context, token string) error {
	if s.logoutFn != nil {
		return s.logoutFn(ctx, token)
	}
	return errUnexpected
}

func (s *stubAuth) LogoutAll(ctx context.Context, uid domain.UUID) (int64, error) {
	if s.logoutAllFn != nil {
		return s.logoutAllFn(ctx, uid)
	}
	return 0, errUnexpected
}

func (s *stubAuth) ChangePassword(ctx context.Context, uid domain.UUID, cur, next string) error {
	if s.changePasswordFn != nil {
		return s.changePasswordFn(ctx, uid, cur, next)
	}
	return errUnexpected
}

func (s *stubAuth) ValidateAccessToken(token string, now time.Time) (*service.TokenClaims, error) {
	if s.validateFn != nil {
		return s.validateFn(token, now)
	}
	if token == testToken {
		return &service.TokenClaims{UserID: testUID, ExpiresAt: now.Add(15 * time.Minute)}, nil
	}
	return nil, service.ErrUnauthorized
}

type stubReset struct {
	forgotFn func(context.Context, string) error
	resetFn  func(context.Context, string, string) error
}

func (s *stubReset) ForgotPassword(ctx context.Context, email string) error {
	if s.forgotFn != nil {
		return s.forgotFn(ctx, email)
	}
	return errUnexpected
}

func (s *stubReset) ResetPassword(ctx context.Context, token, newPassword string) error {
	if s.resetFn != nil {
		return s.resetFn(ctx, token, newPassword)
	}
	return errUnexpected
}

type stubUsers struct {
	profileFn       func(context.Context, domain.UUID) (*domain.User, error)
	updateProfileFn func(context.Context, domain.UUID, *string, *string) (*domain.User, error)
	deleteAccountFn func(context.Context, domain.UUID) error
}

func (s *stubUsers) Profile(ctx context.Context, uid domain.UUID) (*domain.User, error) {
	if s.profileFn != nil {
		return s.profileFn(ctx, uid)
	}
	return nil, errUnexpected
}

func (s *stubUsers) UpdateProfile(ctx context.Context, uid domain.UUID, name, email *string) (*domain.User, error) {
	if s.updateProfileFn != nil {
		return s.updateProfileFn(ctx, uid, name, email)
	}
	return nil, errUnexpected
}

func (s *stubUsers) DeleteAccount(ctx context.Context, uid domain.UUID) error {
	if s.deleteAccountFn != nil {
		return s.deleteAccountFn(ctx, uid)
	}
	return errUnexpected
}

type stubProjects struct {
	createFn func(context.Context, domain.UUID, string, *string) (*domain.Project, error)
	getFn    func(context.Context, domain.UUID, domain.UUID) (*domain.Project, error)
	listFn   func(context.Context, domain.UUID) ([]domain.Project, error)
	updateFn func(context.Context, domain.UUID, domain.UUID, service.ProjectPatch) (*domain.Project, error)
	deleteFn func(context.Context, domain.UUID, domain.UUID) error
}

func (s *stubProjects) Create(ctx context.Context, uid domain.UUID, name string, desc *string) (*domain.Project, error) {
	if s.createFn != nil {
		return s.createFn(ctx, uid, name, desc)
	}
	return nil, errUnexpected
}

func (s *stubProjects) Get(ctx context.Context, uid, pid domain.UUID) (*domain.Project, error) {
	if s.getFn != nil {
		return s.getFn(ctx, uid, pid)
	}
	return nil, errUnexpected
}

func (s *stubProjects) List(ctx context.Context, uid domain.UUID) ([]domain.Project, error) {
	if s.listFn != nil {
		return s.listFn(ctx, uid)
	}
	return nil, errUnexpected
}

func (s *stubProjects) Update(ctx context.Context, uid, pid domain.UUID, patch service.ProjectPatch) (*domain.Project, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, uid, pid, patch)
	}
	return nil, errUnexpected
}

func (s *stubProjects) Delete(ctx context.Context, uid, pid domain.UUID) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, uid, pid)
	}
	return errUnexpected
}

type stubMembers struct {
	listFn     func(context.Context, domain.UUID, domain.UUID) ([]domain.ProjectMember, error)
	addFn      func(context.Context, domain.UUID, domain.UUID, domain.UUID) (*domain.ProjectMember, error)
	roleFn     func(context.Context, domain.UUID, domain.UUID, domain.UUID, domain.Role) error
	removeFn   func(context.Context, domain.UUID, domain.UUID, domain.UUID) error
	transferFn func(context.Context, domain.UUID, domain.UUID, domain.UUID) error
}

func (s *stubMembers) List(ctx context.Context, uid, pid domain.UUID) ([]domain.ProjectMember, error) {
	if s.listFn != nil {
		return s.listFn(ctx, uid, pid)
	}
	return nil, errUnexpected
}

func (s *stubMembers) Add(ctx context.Context, uid, pid, target domain.UUID) (*domain.ProjectMember, error) {
	if s.addFn != nil {
		return s.addFn(ctx, uid, pid, target)
	}
	return nil, errUnexpected
}

func (s *stubMembers) UpdateRole(ctx context.Context, uid, pid, target domain.UUID, role domain.Role) error {
	if s.roleFn != nil {
		return s.roleFn(ctx, uid, pid, target, role)
	}
	return errUnexpected
}

func (s *stubMembers) Remove(ctx context.Context, uid, pid, target domain.UUID) error {
	if s.removeFn != nil {
		return s.removeFn(ctx, uid, pid, target)
	}
	return errUnexpected
}

func (s *stubMembers) TransferOwnership(ctx context.Context, uid, pid, target domain.UUID) error {
	if s.transferFn != nil {
		return s.transferFn(ctx, uid, pid, target)
	}
	return errUnexpected
}

type stubTasks struct {
	createFn   func(context.Context, domain.UUID, domain.UUID, service.TaskInput) (*domain.Task, error)
	getFn      func(context.Context, domain.UUID, domain.UUID) (*domain.Task, error)
	listFn     func(context.Context, domain.UUID, domain.UUID, domain.TaskFilter) ([]domain.Task, error)
	listMineFn func(context.Context, domain.UUID, domain.TaskFilter) ([]domain.Task, error)
	updateFn   func(context.Context, domain.UUID, domain.UUID, service.TaskPatch) (*domain.Task, error)
	deleteFn   func(context.Context, domain.UUID, domain.UUID) error
}

func (s *stubTasks) Create(ctx context.Context, uid, pid domain.UUID, in service.TaskInput) (*domain.Task, error) {
	if s.createFn != nil {
		return s.createFn(ctx, uid, pid, in)
	}
	return nil, errUnexpected
}

func (s *stubTasks) Get(ctx context.Context, uid, tid domain.UUID) (*domain.Task, error) {
	if s.getFn != nil {
		return s.getFn(ctx, uid, tid)
	}
	return nil, errUnexpected
}

func (s *stubTasks) List(ctx context.Context, uid, pid domain.UUID, f domain.TaskFilter) ([]domain.Task, error) {
	if s.listFn != nil {
		return s.listFn(ctx, uid, pid, f)
	}
	return nil, errUnexpected
}

func (s *stubTasks) ListMine(ctx context.Context, uid domain.UUID, f domain.TaskFilter) ([]domain.Task, error) {
	if s.listMineFn != nil {
		return s.listMineFn(ctx, uid, f)
	}
	return nil, errUnexpected
}

func (s *stubTasks) Update(ctx context.Context, uid, tid domain.UUID, patch service.TaskPatch) (*domain.Task, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, uid, tid, patch)
	}
	return nil, errUnexpected
}

func (s *stubTasks) Delete(ctx context.Context, uid, tid domain.UUID) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, uid, tid)
	}
	return errUnexpected
}

// testEnv wires an Echo server with stub services and a captured log buffer.
type testEnv struct {
	e        *echo.Echo
	api      *API
	auth     *stubAuth
	reset    *stubReset
	users    *stubUsers
	projects *stubProjects
	members  *stubMembers
	tasks    *stubTasks
	logs     bytes.Buffer
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	env := &testEnv{
		auth:     &stubAuth{},
		reset:    &stubReset{},
		users:    &stubUsers{},
		projects: &stubProjects{},
		members:  &stubMembers{},
		tasks:    &stubTasks{},
	}
	env.users.profileFn = func(_ context.Context, uid domain.UUID) (*domain.User, error) {
		return &domain.User{ID: uid, Name: "Test User", Email: "test@example.com"}, nil
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewJSONHandler(&env.logs, nil))
	}
	api := New(env.auth, env.reset, env.users, env.projects, env.members, env.tasks)
	env.api = api
	env.e = echo.New()
	api.RegisterRoutes(env.e, opts)
	// Legacy transport tests fire many requests from one peer; rate limiting
	// is exercised explicitly by the dedicated tests via withRateLimit.
	api.authLimiter = nil
	return env
}

// withRateLimit enables the per-IP auth limiter for tests that exercise it.
func (env *testEnv) withRateLimit(interval time.Duration) *testEnv {
	env.api.authLimiter = newAuthRateLimiter(interval)
	return env
}

// do sends a JSON request (body may be nil) with an optional bearer token.
func (env *testEnv) do(t *testing.T, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	return env.doRaw(t, method, path, rdr, echo.MIMEApplicationJSON, token)
}

// doRaw sends a request with full control over body and content type.
func (env *testEnv) doRaw(t *testing.T, method, path string, body io.Reader, contentType, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set(echo.HeaderContentType, contentType)
	}
	if token != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

// decodeEnvelope asserts the response is a well-formed error envelope and
// returns it.
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("error envelope not JSON: %v (body=%q)", err, rec.Body.String())
	}
	if env.Error.Code == "" || env.Error.Message == "" {
		t.Fatalf("envelope missing code/message: %+v", env)
	}
	return env
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, want, rec.Body.String())
	}
}

func assertNoLeak(t *testing.T, body, logs string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(body, s) {
			t.Errorf("response body leaks %q", s)
		}
		if strings.Contains(logs, s) {
			t.Errorf("logs leak %q", s)
		}
	}
}

func sampleUser() *domain.User {
	return &domain.User{ID: testUID, Name: "Test User", Email: "test@example.com", CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

func sampleProject(pid domain.UUID) *domain.Project {
	return &domain.Project{ID: pid, Name: "Project", CreatedBy: testUID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

func sampleTask(tid, pid domain.UUID) *domain.Task {
	return &domain.Task{
		ID: tid, ProjectID: pid, CreatedBy: testUID, Title: "Task",
		Status: domain.StatusTodo, Priority: domain.PriorityMedium,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}
