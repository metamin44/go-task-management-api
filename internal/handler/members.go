package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

type addMemberRequest struct {
	UserID string `json:"user_id"`
}

type updateRoleRequest struct {
	Role string `json:"role"`
}

type transferOwnershipRequest struct {
	NewOwnerID string `json:"new_owner_id"`
}

// listMembers lists memberships and roles (members only).
//
// @Summary      List project members
// @Tags         members
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Success      200 {object} listResponse "data: []memberResponse"
// @Failure      400 {object} errorEnvelope "validation_error: invalid UUID"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: not a member"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/members [get]
func (a *API) listMembers(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	ms, err := a.members.List(c.Request().Context(), actor(c), pid)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, listResponse{Data: toMemberResponses(ms)})
}

// addMember adds an existing user as a member (owner/admin).
//
// @Summary      Add project member
// @Description  Owner/admin only. The target user must exist; new members start with role member.
// @Tags         members
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        body body addMemberRequest true "User to add"
// @Success      201 {object} memberResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden"
// @Failure      404 {object} errorEnvelope "not_found: unknown target user"
// @Failure      409 {object} errorEnvelope "conflict: already a member"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/members [post]
func (a *API) addMember(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	var req addMemberRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.UserID == "" {
		return badRequest("user_id is required")
	}
	target, err := parseUUIDField("user_id", req.UserID)
	if err != nil {
		return err
	}
	m, err := a.members.Add(c.Request().Context(), actor(c), pid, target)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toMemberResponse(m))
}

// updateMemberRole changes a non-owner member between admin and member.
//
// @Summary      Change member role
// @Description  Owner/admin only. Role must be admin or member; the owner role changes exclusively via transfer-ownership.
// @Tags         members
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        userId path string true "Target user UUID"
// @Param        body body updateRoleRequest true "New role (admin|member)"
// @Success      204 "role updated"
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden (includes attempts to demote the owner)"
// @Failure      404 {object} errorEnvelope "not_found: target not a member"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/members/{userId} [patch]
func (a *API) updateMemberRole(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	target, err := pathUUID(c, "userId")
	if err != nil {
		return err
	}
	var req updateRoleRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	role := domain.Role(req.Role)
	if role != domain.RoleAdmin && role != domain.RoleMember {
		return badRequest("role must be admin or member; ownership changes via transfer-ownership")
	}
	if err := a.members.UpdateRole(c.Request().Context(), actor(c), pid, target, role); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// removeMember removes a membership (owner/admin, or self-leave).
//
// @Summary      Remove project member
// @Description  Owner/admin, or non-owner self-leave. 409 while the member has assigned tasks; the owner must transfer ownership first (403).
// @Tags         members
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        userId path string true "Target user UUID"
// @Success      204 "member removed"
// @Failure      400 {object} errorEnvelope "validation_error: invalid UUID"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden"
// @Failure      404 {object} errorEnvelope "not_found: target not a member"
// @Failure      409 {object} errorEnvelope "conflict: assigned tasks exist"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/members/{userId} [delete]
func (a *API) removeMember(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	target, err := pathUUID(c, "userId")
	if err != nil {
		return err
	}
	if err := a.members.Remove(c.Request().Context(), actor(c), pid, target); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// transferOwnership atomically transfers ownership to an existing member.
//
// @Summary      Transfer project ownership
// @Description  Owner only. Transactional demote-before-promote under the project row lock; target must be an existing member; self-transfer is a no-op.
// @Tags         members
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        body body transferOwnershipRequest true "New owner (existing member)"
// @Success      204 "ownership transferred"
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: owner only"
// @Failure      404 {object} errorEnvelope "not_found: target not a member"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/transfer-ownership [post]
func (a *API) transferOwnership(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	var req transferOwnershipRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if req.NewOwnerID == "" {
		return badRequest("new_owner_id is required")
	}
	target, err := parseUUIDField("new_owner_id", req.NewOwnerID)
	if err != nil {
		return err
	}
	if err := a.members.TransferOwnership(c.Request().Context(), actor(c), pid, target); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
