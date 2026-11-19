package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// register creates a new account.
//
// @Summary      Register a new account
// @Description  Creates a user; only an argon2id hash of the password is stored.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body registerRequest true "Account details"
// @Success      201 {object} userResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      409 {object} errorEnvelope "conflict: email already registered"
// @Failure      413 {object} errorEnvelope "payload_too_large"
// @Failure      415 {object} errorEnvelope "unsupported_media_type"
// @Failure      429 {object} errorEnvelope "rate_limited (Retry-After header)"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/auth/register [post]
func (a *API) register(c echo.Context) error {
	var req registerRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || req.Email == "" || req.Password == "" {
		return badRequest("name, email, and password are required")
	}
	if err := domain.ValidateUserName(req.Name); err != nil {
		return badRequest(err.Error()) // domain validator messages are value-free
	}
	if err := domain.ValidateEmail(req.Email); err != nil {
		return badRequest(err.Error())
	}
	if err := service.ValidatePassword(req.Password); err != nil {
		return badRequest(err.Error())
	}
	u, err := a.auth.Register(c.Request().Context(), req.Name, req.Email, req.Password)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toUserResponse(u))
}

// login verifies credentials and issues a token pair.
//
// @Summary      Log in
// @Description  Verifies credentials and issues an access token plus a refresh token with a fresh session. Unknown email and wrong password return the same 401.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body loginRequest true "Credentials"
// @Success      200 {object} tokenPairResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      429 {object} errorEnvelope "rate_limited (Retry-After header)"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/auth/login [post]
func (a *API) login(c echo.Context) error {
	var req loginRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.Email == "" || req.Password == "" {
		return badRequest("email and password are required")
	}
	pair, err := a.auth.Login(c.Request().Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toTokenPairResponse(pair))
}

// refresh rotates the presented refresh token.
//
// @Summary      Refresh tokens
// @Description  Rotates the refresh token (row-locked, reuse-detecting). Reuse revokes the whole session and returns 401 with code token_reused.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body refreshTokenRequest true "Current refresh token"
// @Success      200 {object} tokenPairResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized or token_reused"
// @Failure      429 {object} errorEnvelope "rate_limited (Retry-After header)"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Failure      503 {object} errorEnvelope "unavailable: contention, retry"
// @Router       /api/v1/auth/refresh [post]
func (a *API) refresh(c echo.Context) error {
	var req refreshTokenRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.RefreshToken == "" {
		return badRequest("refresh_token is required")
	}
	pair, err := a.auth.Refresh(c.Request().Context(), req.RefreshToken)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toTokenPairResponse(pair))
}

// logout revokes the session of the presented refresh token.
//
// @Summary      Log out
// @Description  Revokes the session of the presented refresh token. Idempotent for already-revoked tokens.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body refreshTokenRequest true "Current refresh token"
// @Success      204 "session revoked"
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized: unknown token"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/auth/logout [post]
func (a *API) logout(c echo.Context) error {
	var req refreshTokenRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.RefreshToken == "" {
		return badRequest("refresh_token is required")
	}
	if err := a.auth.Logout(c.Request().Context(), req.RefreshToken); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// logoutAll revokes every session of the authenticated user.
//
// @Summary      Log out all sessions
// @Description  Revokes all active refresh sessions of the authenticated user.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} logoutAllResponse
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/auth/logout-all [post]
func (a *API) logoutAll(c echo.Context) error {
	n, err := a.auth.LogoutAll(c.Request().Context(), actor(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, logoutAllResponse{RevokedSessions: n})
}
