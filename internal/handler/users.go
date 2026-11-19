package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

type updateProfileRequest struct {
	Name  *string `json:"name"`
	Email *string `json:"email"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// getProfile returns the authenticated user's profile.
//
// @Summary      Get current profile
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} userResponse
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/users/me [get]
func (a *API) getProfile(c echo.Context) error {
	u, err := a.users.Profile(c.Request().Context(), actor(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toUserResponse(u))
}

// updateProfile patches name and/or email; omitted fields keep their values.
//
// @Summary      Update current profile
// @Description  Partial update of name/email. Email is normalized; duplicates return 409.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body updateProfileRequest true "Fields to update (at least one)"
// @Success      200 {object} userResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      409 {object} errorEnvelope "conflict: email in use or concurrently modified"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/users/me [patch]
func (a *API) updateProfile(c echo.Context) error {
	var req updateProfileRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.Name == nil && req.Email == nil {
		return badRequest("at least one of name or email is required")
	}
	// Validate only the supplied fields; omitted fields are merged by the
	// service on its freshest read (no stale handler-side read-and-merge).
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if err := domain.ValidateUserName(n); err != nil {
			return badRequest(err.Error())
		}
		req.Name = &n
	}
	if req.Email != nil {
		if err := domain.ValidateEmail(*req.Email); err != nil {
			return badRequest(err.Error())
		}
	}
	u, err := a.users.UpdateProfile(c.Request().Context(), actor(c), req.Name, req.Email)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toUserResponse(u))
}

// changePassword verifies the current password and revokes all sessions.
//
// @Summary      Change password
// @Description  Verifies the current password, stores the new argon2id hash, and revokes ALL refresh sessions atomically.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body changePasswordRequest true "Current and new password"
// @Success      204 "password changed; all sessions revoked"
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized: wrong current password"
// @Failure      429 {object} errorEnvelope "rate_limited (Retry-After header)"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/users/me/password [patch]
func (a *API) changePassword(c echo.Context) error {
	var req changePasswordRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		return badRequest("current_password and new_password are required")
	}
	if err := service.ValidatePassword(req.NewPassword); err != nil {
		return badRequest(err.Error())
	}
	if err := a.auth.ChangePassword(c.Request().Context(), actor(c), req.CurrentPassword, req.NewPassword); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// deleteAccount hard-deletes the authenticated user.
//
// @Summary      Delete account
// @Description  Hard delete. Returns 409 while created projects/tasks or memberships exist (RESTRICT chain).
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Success      204 "account deleted"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      409 {object} errorEnvelope "reference_violation"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/users/me [delete]
func (a *API) deleteAccount(c echo.Context) error {
	if err := a.users.DeleteAccount(c.Request().Context(), actor(c)); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
