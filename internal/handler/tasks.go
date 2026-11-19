package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

type createTaskRequest struct {
	Title       string     `json:"title"`
	Description *string    `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	AssignedTo  *string    `json:"assigned_to"`
	DueAt       *time.Time `json:"due_at"`
}

type updateTaskRequest struct {
	Title *string `json:"title"`
	// String only; null is rejected (service cannot NULL the description).
	Description json.RawMessage `json:"description" swaggertype:"string"`
	Status      *string         `json:"status"`
	Priority    *string         `json:"priority"`
	// Assignee UUID string, or null to unassign; omitted keeps it.
	AssignedTo json.RawMessage `json:"assigned_to" swaggertype:"string"`
	// RFC3339 timestamp string, or null to clear; omitted keeps it.
	DueAt json.RawMessage `json:"due_at" swaggertype:"string"`
}

// createTask adds a task to a project (any member).
//
// @Summary      Create task
// @Description  Any project member may create. Assignee, when set, must be a member of the same project. Defaults: status=todo, priority=medium.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        body body createTaskRequest true "Task details"
// @Success      201 {object} taskResponse
// @Failure      400 {object} errorEnvelope "validation_error (incl. non-member assignee)"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: not a member"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/tasks [post]
func (a *API) createTask(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	var req createTaskRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	if err := domain.ValidateTaskTitle(req.Title); err != nil {
		return badRequest(err.Error())
	}
	in := service.TaskInput{
		Title:       req.Title,
		Description: req.Description,
		Status:      domain.Status(req.Status),
		Priority:    domain.Priority(req.Priority),
		DueAt:       req.DueAt,
	}
	if req.Status != "" && !in.Status.Valid() {
		return badRequest("status must be one of: todo, in_progress, done")
	}
	if req.Priority != "" && !in.Priority.Valid() {
		return badRequest("priority must be one of: low, medium, high, urgent")
	}
	if req.AssignedTo != nil {
		uid, err := parseUUIDField("assigned_to", *req.AssignedTo)
		if err != nil {
			return err
		}
		in.AssignedTo = &uid
	}
	t, err := a.tasks.Create(c.Request().Context(), actor(c), pid, in)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toTaskResponse(t))
}

// listTasks lists project tasks with filtering, sorting, and pagination.
//
// @Summary      List project tasks
// @Description  Members only. Filters: status, priority, assigned_to, due_after, due_before (RFC3339). Sorting: sort_by=created_at|due_at|priority|status, sort_order=asc|desc. Pagination: limit (1-200, default 50), offset (>=0).
// @Tags         tasks
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        limit query int false "Page size (1-200, default 50)"
// @Param        offset query int false "Offset (default 0)"
// @Param        status query string false "todo|in_progress|done"
// @Param        priority query string false "low|medium|high|urgent"
// @Param        assigned_to query string false "Assignee UUID"
// @Param        due_after query string false "RFC3339 lower bound"
// @Param        due_before query string false "RFC3339 upper bound"
// @Param        sort_by query string false "created_at|due_at|priority|status"
// @Param        sort_order query string false "asc|desc"
// @Success      200 {object} listResponse "data: []taskResponse, limit, offset"
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: not a member"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/tasks [get]
func (a *API) listTasks(c echo.Context) error {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return err
	}
	f, err := taskFilterFromQuery(c, false)
	if err != nil {
		return err
	}
	ts, err := a.tasks.List(c.Request().Context(), actor(c), pid, f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, listResponse{Data: toTaskResponses(ts), Limit: &f.Limit, Offset: &f.Offset})
}

// myTasks lists the caller's assigned tasks across projects.
//
// @Summary      List my assigned tasks
// @Description  Cross-project listing forced to the authenticated assignee. Same filtering/sorting/pagination as project task lists, plus optional project_id.
// @Tags         tasks
// @Produce      json
// @Security     BearerAuth
// @Param        project_id query string false "Restrict to one project UUID"
// @Param        limit query int false "Page size (1-200, default 50)"
// @Param        offset query int false "Offset (default 0)"
// @Param        status query string false "todo|in_progress|done"
// @Param        priority query string false "low|medium|high|urgent"
// @Param        due_after query string false "RFC3339 lower bound"
// @Param        due_before query string false "RFC3339 upper bound"
// @Param        sort_by query string false "created_at|due_at|priority|status"
// @Param        sort_order query string false "asc|desc"
// @Success      200 {object} listResponse "data: []taskResponse, limit, offset"
// @Failure      400 {object} errorEnvelope "validation_error"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: not a member of project_id"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/users/me/tasks [get]
func (a *API) myTasks(c echo.Context) error {
	f, err := taskFilterFromQuery(c, true)
	if err != nil {
		return err
	}
	ts, err := a.tasks.ListMine(c.Request().Context(), actor(c), f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, listResponse{Data: toTaskResponses(ts), Limit: &f.Limit, Offset: &f.Offset})
}

// getTask returns task detail (members of its project only).
//
// @Summary      Get task
// @Tags         tasks
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        taskId path string true "Task UUID"
// @Success      200 {object} taskResponse
// @Failure      400 {object} errorEnvelope "validation_error: invalid UUID"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden: not a member"
// @Failure      404 {object} errorEnvelope "not_found (also when the task belongs to another project)"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/tasks/{taskId} [get]
func (a *API) getTask(c echo.Context) error {
	t, err := a.taskInProject(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toTaskResponse(t))
}

// updateTask patches a task (creator/assignee/owner/admin).
//
// @Summary      Update task
// @Description  Write policy: task creator, current assignee, or project owner/admin. assigned_to=null unassigns; due_at=null clears the due date; omitted fields are unchanged. project_id and created_by are immutable.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        taskId path string true "Task UUID"
// @Param        body body updateTaskRequest true "Fields to update"
// @Success      200 {object} taskResponse
// @Failure      400 {object} errorEnvelope "validation_error (incl. non-member assignee)"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden"
// @Failure      404 {object} errorEnvelope "not_found"
// @Failure      409 {object} errorEnvelope "conflict: concurrently modified"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/tasks/{taskId} [patch]
func (a *API) updateTask(c echo.Context) error {
	current, err := a.taskInProject(c)
	if err != nil {
		return err
	}
	var req updateTaskRequest
	if err := bindJSON(c, &req); err != nil {
		return err
	}
	var patch service.TaskPatch
	if req.Title != nil {
		if err := domain.ValidateTaskTitle(*req.Title); err != nil {
			return badRequest(err.Error())
		}
		patch.Title = req.Title
	}
	if len(req.Description) > 0 {
		if string(req.Description) == "null" {
			// Service limitation (Phase 2): TaskPatch cannot set description
			// to NULL; reject explicitly instead of silently ignoring.
			return badRequest("description cannot be null; omit it to keep the current value")
		}
		var s string
		if err := json.Unmarshal(req.Description, &s); err != nil {
			return badRequest("description must be a string")
		}
		patch.Description = &s
	}
	if req.Status != nil {
		s := domain.Status(*req.Status)
		if !s.Valid() {
			return badRequest("status must be one of: todo, in_progress, done")
		}
		patch.Status = &s
	}
	if req.Priority != nil {
		p := domain.Priority(*req.Priority)
		if !p.Valid() {
			return badRequest("priority must be one of: low, medium, high, urgent")
		}
		patch.Priority = &p
	}
	assigned, assignedPresent, err := nullableUUID(req.AssignedTo, "assigned_to")
	if err != nil {
		return err
	}
	if assignedPresent {
		if assigned == nil {
			patch.Unassign = true
		} else {
			patch.AssignedTo = assigned
		}
	}
	due, duePresent, err := nullableTime(req.DueAt, "due_at")
	if err != nil {
		return err
	}
	if duePresent {
		if due == nil {
			patch.ClearDueAt = true
		} else {
			patch.DueAt = due
		}
	}
	t, err := a.tasks.Update(c.Request().Context(), actor(c), current.ID, patch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toTaskResponse(t))
}

// deleteTask removes a task (creator/assignee/owner/admin).
//
// @Summary      Delete task
// @Description  Write policy: task creator, current assignee, or project owner/admin.
// @Tags         tasks
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "Project UUID"
// @Param        taskId path string true "Task UUID"
// @Success      204 "task deleted"
// @Failure      400 {object} errorEnvelope "validation_error: invalid UUID"
// @Failure      401 {object} errorEnvelope "unauthorized"
// @Failure      403 {object} errorEnvelope "forbidden"
// @Failure      404 {object} errorEnvelope "not_found"
// @Failure      500 {object} errorEnvelope "internal_error"
// @Router       /api/v1/projects/{id}/tasks/{taskId} [delete]
func (a *API) deleteTask(c echo.Context) error {
	t, err := a.taskInProject(c)
	if err != nil {
		return err
	}
	if err := a.tasks.Delete(c.Request().Context(), actor(c), t.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// taskInProject loads the task via the service (which enforces membership)
// and verifies it belongs to the URL's project; mismatches are 404 so task
// IDs cannot be probed across projects.
func (a *API) taskInProject(c echo.Context) (*domain.Task, error) {
	pid, err := pathUUID(c, "id")
	if err != nil {
		return nil, err
	}
	taskID, err := pathUUID(c, "taskId")
	if err != nil {
		return nil, err
	}
	t, err := a.tasks.Get(c.Request().Context(), actor(c), taskID)
	if err != nil {
		return nil, err
	}
	if t.ProjectID != pid {
		return nil, &apiError{http.StatusNotFound, "not_found", "resource not found"}
	}
	return t, nil
}
