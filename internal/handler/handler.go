// Package handler implements the HTTP API layer (Echo v4). It translates
// transport concerns — JSON DTOs, validation, status codes, authentication
// middleware, request IDs, structured logging, panic recovery, and CORS —
// into calls on the service layer. All authorization decisions remain in the
// service layer; handlers never trust client-supplied user IDs.
//
// SECURITY: passwords, tokens, authorization headers, and request bodies are
// never logged. Error responses use static, safe messages only — never SQL,
// driver text, stack traces, or secrets.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// Consumer-defined interfaces over the service layer (Go idiom: accept
// interfaces). The concrete services satisfy them implicitly; tests inject
// focused stubs without a database.
type AuthAPI interface {
	Register(ctx context.Context, name, email, password string) (*domain.User, error)
	Login(ctx context.Context, email, password string) (*service.TokenPair, error)
	Refresh(ctx context.Context, refreshToken string) (*service.TokenPair, error)
	Logout(ctx context.Context, refreshToken string) error
	LogoutAll(ctx context.Context, userID domain.UUID) (int64, error)
	ChangePassword(ctx context.Context, userID domain.UUID, currentPassword, newPassword string) error
	ValidateAccessToken(token string, now time.Time) (*service.TokenClaims, error)
}

type UserAPI interface {
	Profile(ctx context.Context, userID domain.UUID) (*domain.User, error)
	UpdateProfile(ctx context.Context, userID domain.UUID, name, email *string) (*domain.User, error)
	DeleteAccount(ctx context.Context, userID domain.UUID) error
}

type ProjectAPI interface {
	Create(ctx context.Context, actorID domain.UUID, name string, description *string) (*domain.Project, error)
	Get(ctx context.Context, actorID, projectID domain.UUID) (*domain.Project, error)
	List(ctx context.Context, actorID domain.UUID) ([]domain.Project, error)
	Update(ctx context.Context, actorID, projectID domain.UUID, patch service.ProjectPatch) (*domain.Project, error)
	Delete(ctx context.Context, actorID, projectID domain.UUID) error
}

type MemberAPI interface {
	List(ctx context.Context, actorID, projectID domain.UUID) ([]domain.ProjectMember, error)
	Add(ctx context.Context, actorID, projectID, targetUserID domain.UUID) (*domain.ProjectMember, error)
	UpdateRole(ctx context.Context, actorID, projectID, targetUserID domain.UUID, newRole domain.Role) error
	Remove(ctx context.Context, actorID, projectID, targetUserID domain.UUID) error
	TransferOwnership(ctx context.Context, actorID, projectID, newOwnerID domain.UUID) error
}

type TaskAPI interface {
	Create(ctx context.Context, actorID, projectID domain.UUID, in service.TaskInput) (*domain.Task, error)
	Get(ctx context.Context, actorID, taskID domain.UUID) (*domain.Task, error)
	List(ctx context.Context, actorID, projectID domain.UUID, f domain.TaskFilter) ([]domain.Task, error)
	ListMine(ctx context.Context, actorID domain.UUID, f domain.TaskFilter) ([]domain.Task, error)
	Update(ctx context.Context, actorID, taskID domain.UUID, patch service.TaskPatch) (*domain.Task, error)
	Delete(ctx context.Context, actorID, taskID domain.UUID) error
}

var (
	_ AuthAPI    = (*service.AuthService)(nil)
	_ UserAPI    = (*service.UserService)(nil)
	_ ProjectAPI = (*service.ProjectService)(nil)
	_ MemberAPI  = (*service.MemberService)(nil)
	_ TaskAPI    = (*service.TaskService)(nil)
)

// API bundles the service dependencies for all HTTP handlers.
type API struct {
	auth        AuthAPI
	users       UserAPI
	projects    ProjectAPI
	members     MemberAPI
	tasks       TaskAPI
	logger      *slog.Logger
	authLimiter *authRateLimiter
}

// New wires the API. Any nil dependency is a programming error and panics
// immediately at composition time (never at request time).
func New(auth AuthAPI, users UserAPI, projects ProjectAPI, members MemberAPI, tasks TaskAPI) *API {
	if auth == nil || users == nil || projects == nil || members == nil || tasks == nil {
		panic("handler: nil service dependency")
	}
	return &API{auth: auth, users: users, projects: projects, members: members, tasks: tasks}
}

// Options and RegisterRoutes live in router.go (route registration and
// route-specific middleware wiring).

// health is a liveness probe.
//
// @Summary      Liveness probe
// @Tags         health
// @Produce      json
// @Success      200 {object} statusResponse
// @Router       /health [get]
func (a *API) health(c echo.Context) error {
	return c.JSON(http.StatusOK, statusResponse{Status: "ok"})
}

// ready reports readiness via the injected check.
//
// @Summary      Readiness probe
// @Tags         health
// @Produce      json
// @Success      200 {object} statusResponse
// @Failure      503 {object} errorEnvelope
// @Router       /ready [get]
func (a *API) ready(check func(context.Context) error) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := check(c.Request().Context()); err != nil {
			return c.JSON(http.StatusServiceUnavailable, statusResponse{Status: "unavailable"})
		}
		return c.JSON(http.StatusOK, statusResponse{Status: "ok"})
	}
}

// Context keys and accessors. The authenticated user is stored by the auth
// middleware only; handlers read the actor exclusively from here.
const (
	requestIDKey = "handler.requestID"
	authUserKey  = "handler.authUser"
)

func requestIDFrom(c echo.Context) string {
	id, _ := c.Get(requestIDKey).(string)
	return id
}

// actor returns the authenticated user's ID (set by requireAuth).
func actor(c echo.Context) domain.UUID {
	u, _ := c.Get(authUserKey).(*domain.User)
	if u == nil {
		// Programming error: route registered without requireAuth.
		panic("handler: actor() on unauthenticated route")
	}
	return u.ID
}
