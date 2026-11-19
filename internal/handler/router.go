package handler

// Route registration and route-specific middleware wiring. Handler methods
// live in their per-resource files; middleware implementations live in
// middleware.go; this file only composes them onto the Echo instance.

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"
)

// Options controls cross-cutting middleware. No configuration loading happens
// here; the composition root (later phase) supplies these values.
type Options struct {
	// Logger receives structured request logs. Nil discards all logs.
	Logger *slog.Logger
	// AllowedOrigins is an explicit CORS whitelist. Empty (the default)
	// installs no CORS middleware: browsers get no cross-origin access.
	// Wildcard origins must never be passed here.
	AllowedOrigins []string
	// ReadinessCheck, when set, backs GET /ready (e.g. a DB ping wired by
	// the composition root). Nil leaves /ready unregistered.
	ReadinessCheck func(context.Context) error
	// AuthRateLimitInterval is the minimum average interval between expensive
	// authentication requests per client IP (register/login/refresh/change-
	// password). Zero uses the conservative default (1s). Limits are
	// per-instance; multi-replica deployments should also limit at the proxy.
	AuthRateLimitInterval time.Duration
}

// RegisterRoutes installs middleware and all routes on e.
func (a *API) RegisterRoutes(e *echo.Echo, opts Options) {
	a.logger = opts.Logger
	if a.logger == nil {
		a.logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	a.authLimiter = newAuthRateLimiter(opts.AuthRateLimitInterval)
	e.HTTPErrorHandler = a.httpErrorHandler

	e.Use(a.requestIDMiddleware)
	e.Use(a.accessLogMiddleware)
	e.Use(a.recoverMiddleware)
	if len(opts.AllowedOrigins) > 0 {
		e.Use(a.corsMiddleware(opts.AllowedOrigins))
	}

	e.GET("/health", a.health)
	if opts.ReadinessCheck != nil {
		e.GET("/ready", a.ready(opts.ReadinessCheck))
	}

	v1 := e.Group("/api/v1")

	// Public authentication endpoints (credentials or refresh token based).
	// The expensive operations are rate-limited per client IP.
	auth := v1.Group("/auth")
	auth.POST("/register", a.limitAuth(a.register))
	auth.POST("/login", a.limitAuth(a.login))
	auth.POST("/refresh", a.limitAuth(a.refresh))
	auth.POST("/logout", a.logout)

	// Everything below requires a valid access token; the actor identity is
	// loaded server-side and never taken from the request payload.
	authed := v1.Group("", a.requireAuth)
	authed.POST("/auth/logout-all", a.logoutAll)

	me := authed.Group("/users/me")
	me.GET("", a.getProfile)
	me.PATCH("", a.updateProfile)
	me.PATCH("/password", a.limitAuth(a.changePassword))
	me.DELETE("", a.deleteAccount)
	me.GET("/tasks", a.myTasks)

	projects := authed.Group("/projects")
	projects.POST("", a.createProject)
	projects.GET("", a.listProjects)
	projects.GET("/:id", a.getProject)
	projects.PATCH("/:id", a.updateProject)
	projects.DELETE("/:id", a.deleteProject)

	projects.GET("/:id/members", a.listMembers)
	projects.POST("/:id/members", a.addMember)
	projects.PATCH("/:id/members/:userId", a.updateMemberRole)
	projects.DELETE("/:id/members/:userId", a.removeMember)
	projects.POST("/:id/transfer-ownership", a.transferOwnership)

	projects.POST("/:id/tasks", a.createTask)
	projects.GET("/:id/tasks", a.listTasks)
	projects.GET("/:id/tasks/:taskId", a.getTask)
	projects.PATCH("/:id/tasks/:taskId", a.updateTask)
	projects.DELETE("/:id/tasks/:taskId", a.deleteTask)
}
