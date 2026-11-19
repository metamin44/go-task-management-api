package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// errorEnvelope is the single JSON error shape used by every failure path
// (handlers, middleware, unmatched routes, panics).
type errorEnvelope struct {
	Error     errorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiError is a transport-level error with an explicit, client-safe status,
// machine-readable code, and static message. Messages must never contain
// user input values, SQL, driver text, or secrets.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func badRequest(message string) *apiError {
	return &apiError{http.StatusBadRequest, "validation_error", message}
}

func unauthorized(message string) *apiError {
	return &apiError{http.StatusUnauthorized, "unauthorized", message}
}

// mapError converts any error into a client-safe apiError. Only known
// sentinels are translated; everything else collapses to a static 500 so
// internal details (SQL, constraint names, DSNs, stack info) never leak.
func mapError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}

	var he *echo.HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusNotFound:
			return &apiError{http.StatusNotFound, "not_found", "route not found"}
		case http.StatusMethodNotAllowed:
			return &apiError{http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed for this route"}
		case http.StatusRequestEntityTooLarge:
			return &apiError{http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"}
		case http.StatusUnsupportedMediaType:
			return &apiError{http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json"}
		default:
			return &apiError{http.StatusInternalServerError, "internal_error", "internal server error"}
		}
	}

	switch {
	case errors.Is(err, service.ErrTokenReused):
		return &apiError{http.StatusUnauthorized, "token_reused", "refresh token reuse detected; log in again"}
	case errors.Is(err, service.ErrUnauthorized):
		return unauthorized("invalid credentials or token")
	case errors.Is(err, service.ErrForbidden):
		return &apiError{http.StatusForbidden, "forbidden", "you do not have permission to perform this operation"}
	case errors.Is(err, domain.ErrNotFound):
		return &apiError{http.StatusNotFound, "not_found", "resource not found"}
	case errors.Is(err, domain.ErrConflict):
		return &apiError{http.StatusConflict, "conflict", "resource state conflicts with this operation"}
	case errors.Is(err, domain.ErrReference):
		return &apiError{http.StatusConflict, "reference_violation", "other resources reference this record; remove them first"}
	case errors.Is(err, domain.ErrInvalid):
		return badRequest("invalid input")
	case errors.Is(err, domain.ErrSerialization):
		return &apiError{http.StatusServiceUnavailable, "unavailable", "temporary contention; retry the request"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &apiError{http.StatusServiceUnavailable, "unavailable", "request canceled or timed out"}
	default:
		return &apiError{http.StatusInternalServerError, "internal_error", "internal server error"}
	}
}

// httpErrorHandler is Echo's central error handler: every failed request ends
// here and receives the consistent JSON envelope.
func (a *API) httpErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	ae := mapError(err)
	resp := errorEnvelope{
		Error:     errorBody{Code: ae.code, Message: ae.message},
		RequestID: requestIDFrom(c),
	}
	if c.Request().Method == http.MethodHead {
		_ = c.NoContent(ae.status)
		return
	}
	_ = c.JSON(ae.status, resp)
}
