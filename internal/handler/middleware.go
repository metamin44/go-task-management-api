package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

const headerRequestID = "X-Request-ID"

// requestIDMiddleware assigns a fresh server-generated request ID (client
// values are never trusted), exposes it on the response, and stores it for
// error envelopes and logs.
func (a *API) requestIDMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		id := domain.NewUUID().String()
		c.Set(requestIDKey, id)
		c.Response().Header().Set(headerRequestID, id)
		return next(c)
	}
}

// accessLogMiddleware logs request metadata only: request ID, method, route
// pattern, status, and duration. Headers, query strings, bodies, tokens, and
// passwords are never logged.
func (a *API) accessLogMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		status := c.Response().Status
		if err != nil {
			// The central error handler runs after middleware unwinds;
			// derive the final status from the mapped error.
			status = mapError(err).status
		}
		path := c.Path()
		if path == "" {
			path = c.Request().URL.Path // no query string
		}
		a.logger.Info("http request",
			"request_id", requestIDFrom(c),
			"method", c.Request().Method,
			"path", path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return err
	}
}

// recoverMiddleware converts panics into a static 500. The panic value and
// stack are deliberately NOT logged or returned: either could contain
// sensitive data. Only request metadata is recorded.
func (a *API) recoverMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				a.logger.Error("panic recovered",
					"request_id", requestIDFrom(c),
					"method", c.Request().Method,
					"path", c.Path(),
				)
				err = &apiError{http.StatusInternalServerError, "internal_error", "internal server error"}
			}
		}()
		return next(c)
	}
}

// corsMiddleware is a minimal, conservative CORS whitelist implemented here
// (instead of echo/middleware, which pulls an unapproved transitive
// dependency). Only explicitly listed origins receive CORS headers;
// credentials are never allowed and there is no wildcard. Preflight requests
// from allowed origins are answered before routing/authentication.
func (a *API) corsMiddleware(allowedOrigins []string) echo.MiddlewareFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			origin := c.Request().Header.Get(echo.HeaderOrigin)
			if origin == "" || !allowed[origin] {
				return next(c)
			}
			h := c.Response().Header()
			h.Set(echo.HeaderAccessControlAllowOrigin, origin)
			h.Add(echo.HeaderVary, echo.HeaderOrigin)
			if c.Request().Method != http.MethodOptions {
				return next(c)
			}
			h.Set(echo.HeaderAccessControlAllowMethods, "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set(echo.HeaderAccessControlAllowHeaders, "Authorization, Content-Type, "+headerRequestID)
			h.Set(echo.HeaderAccessControlMaxAge, "600")
			h.Add(echo.HeaderVary, echo.HeaderAccessControlRequestMethod)
			h.Add(echo.HeaderVary, echo.HeaderAccessControlRequestHeaders)
			return c.NoContent(http.StatusNoContent)
		}
	}
}

// requireAuth validates the Bearer access token via the auth service, then
// loads the user's current identity through the user service (a deleted
// account cannot keep using a still-valid token). The identity is stored in
// the request context; handlers never accept actor IDs from client input.
func (a *API) requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		const prefix = "Bearer "
		h := c.Request().Header.Get(echo.HeaderAuthorization)
		if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
			return unauthorized("missing or malformed bearer token")
		}
		token := strings.TrimSpace(h[len(prefix):])
		if token == "" {
			return unauthorized("missing or malformed bearer token")
		}
		claims, err := a.auth.ValidateAccessToken(token, time.Now())
		if err != nil {
			return unauthorized("invalid or expired access token")
		}
		u, err := a.users.Profile(c.Request().Context(), claims.UserID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return unauthorized("account no longer exists")
			}
			return err
		}
		c.Set(authUserKey, u)
		return next(c)
	}
}
