package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

type createProjectRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type updateProjectRequest struct {
	Name *string `json:"name"`
	// String or null (null clears the description); omitted keeps it.
	Description json.RawMessage `json:"description" swaggertype:"string"`
}

// createProject creates a project with the caller as owner (one transaction).
//
// @Summary      Create project
// @Description  Atomically creates the project and the creator's owner membership.
// @Tags         projects
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body createProjectRequest true "Project details"
// @Success      201 {object} projectResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects [post]
func (a *API) createProject(c echo.Context) error {
	var req createProjectRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := domain.ValidateProjectName(req.Name); err != nil {
		return badRequest(err.Error())
	}
	p, err := a.projects.Create(c.Request().Context(), actor(c), req.Name, req.Description)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toProjectResponse(p))
}

// listProjects lists projects where the caller is a member.
//
// @Summary      List my projects
// @Tags         projects
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} listResponse "data: []projectResponse"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects [get]
func (a *API) listProjects(c echo.Context) error {
	ps, err := a.projects.List(c.Request().Context(), actor(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, listResponse{Data: toProjectResponses(ps)})
}

// getProject returns project detail (members only).
//
// @Summary      Get project
// @Tags         projects
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Success      200 {object} projectResponse
// @Failure      400 {object} errorEnvelope "validation_error: invalid UUID"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: not a member"
// @Failure      404 {object} errorEnvelope "not_found"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id} [get]
func (a *API) getProject(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	p, err := a.projects.Get(c.Request().Context(), actor(c), pid)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toProjectResponse(p))
}

// updateProject patches name/description (owner/admin).
//
// @Summary      Update project
// @Description  Owner/admin only. Omitted fields keep their values; description=null clears it.
// @Tags         projects
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        body body updateProjectRequest true "Fields to update"
// @Success      200 {object} projectResponse
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden"
// @Failure      404 {object} errorEnvelope "not_found"
// @Failure      409 {object} errorEnvelope "conflict: concurrently modified"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id} [patch]
func (a *API) updateProject(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	var req updateProjectRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	desc, descPresent, err := nullableString(req.Description, "description")
	if err != nil {
		return err
	}
	if req.Name == nil && !descPresent {
		return badRequest("at least one of name or description is required")
	}
	// Validate only the supplied name; omitted fields are merged by the
	// service on its freshest read (no stale handler-side read-and-merge).
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if err := domain.ValidateProjectName(n); err != nil {
			return badRequest(err.Error())
		}
		req.Name = &n
	}
	patch := service.ProjectPatch{Name: req.Name, Description: desc, DescriptionSet: descPresent}
	p, err := a.projects.Update(c.Request().Context(), actor(c), pid, patch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toProjectResponse(p))
}

// deleteProject deletes a project (owner only; cascades members and tasks).
//
// @Summary      Delete project
// @Description  Owner only. Memberships and tasks cascade.
// @Tags         projects
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Success      204 "project deleted"
// @Failure      400 {object} errorEnvelope "validation_error: invalid UUID"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: owner only"
// @Failure      404 {object} errorEnvelope "not_found"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id} [delete]
func (a *API) deleteProject(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	if err := a.projects.Delete(c.Request().Context(), actor(c), pid); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
