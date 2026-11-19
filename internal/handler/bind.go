package handler

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// maxBodyBytes bounds every JSON request body (1 MiB).
const maxBodyBytes = 1 << 20

// bindJSON decodes a single JSON object strictly: application/json only,
// size-limited, unknown fields rejected, trailing data rejected. Error
// messages are static — client input is never echoed.
func bindJSON(c echo.Context, dst any) error {
	req := c.Request()
	mt, _, err := mime.ParseMediaType(req.Header.Get(echo.HeaderContentType))
	if err != nil || mt != echo.MIMEApplicationJSON {
		return &apiError{http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json"}
	}
	if req.Body == nil {
		return badRequest("request body required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), req.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &apiError{http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"}
		}
		if errors.Is(err, io.EOF) {
			return badRequest("request body required")
		}
		if strings.Contains(err.Error(), "unknown field") {
			return badRequest("request body contains unknown fields")
		}
		return badRequest("malformed JSON request body")
	}
	if dec.More() {
		return badRequest("request body must contain a single JSON object")
	}
	return nil
}

// pathUUID parses a UUID path parameter; failures are 400 with a static,
// field-named message (the supplied value is never echoed).
func pathUUID(c echo.Context, name string) (domain.UUID, error) {
	id, err := domain.ParseUUID(c.Param(name))
	if err != nil {
		return domain.UUID{}, badRequest("invalid " + name + " path parameter: expected a UUID")
	}
	return id, nil
}

// parseUUIDField parses an explicit UUID string field.
func parseUUIDField(field, value string) (domain.UUID, error) {
	id, err := domain.ParseUUID(value)
	if err != nil {
		return domain.UUID{}, badRequest(field + " must be a valid UUID")
	}
	return id, nil
}

// nullableString distinguishes absent / null / string for PATCH fields.
// present=false means the field was omitted; a nil value with present=true
// means JSON null. Non-string, non-null payloads are rejected.
func nullableString(raw json.RawMessage, field string) (value *string, present bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false, badRequest(field + " must be a string or null")
	}
	return &s, true, nil
}

// nullableUUID is nullableString plus UUID parsing.
func nullableUUID(raw json.RawMessage, field string) (value *domain.UUID, present bool, err error) {
	s, present, err := nullableString(raw, field)
	if err != nil || !present || s == nil {
		return nil, present, err
	}
	id, err := parseUUIDField(field, *s)
	if err != nil {
		return nil, false, err
	}
	return &id, true, nil
}

// nullableTime is nullableString plus RFC3339 parsing.
func nullableTime(raw json.RawMessage, field string) (value *time.Time, present bool, err error) {
	s, present, err := nullableString(raw, field)
	if err != nil || !present || s == nil {
		return nil, present, err
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, false, badRequest(field + " must be an RFC3339 timestamp")
	}
	return &t, true, nil
}

// taskQueryParams is the whitelist of accepted task-list query parameters.
var taskQueryParams = map[string]bool{
	"limit": true, "offset": true,
	"status": true, "priority": true, "assigned_to": true,
	"due_after": true, "due_before": true,
	"sort_by": true, "sort_order": true,
}

// taskFilterFromQuery parses and validates task-list query parameters.
// Unknown or duplicated parameters are rejected. When allowProject is true
// (cross-project "my tasks"), project_id is also accepted.
func taskFilterFromQuery(c echo.Context, allowProject bool) (domain.TaskFilter, error) {
	var f domain.TaskFilter
	f.Limit = 50
	f.SortBy = domain.TaskSortCreatedAt

	for key, values := range c.QueryParams() {
		if !taskQueryParams[key] && !(allowProject && key == "project_id") {
			return f, badRequest("unknown query parameter: " + key)
		}
		if len(values) != 1 {
			return f, badRequest("query parameter " + key + " must appear exactly once")
		}
		v := values[0]
		var err error
		switch key {
		case "limit":
			f.Limit, err = strconv.Atoi(v)
			if err != nil || f.Limit < 1 || f.Limit > 200 {
				return f, badRequest("limit must be an integer between 1 and 200")
			}
		case "offset":
			f.Offset, err = strconv.Atoi(v)
			if err != nil || f.Offset < 0 {
				return f, badRequest("offset must be a non-negative integer")
			}
		case "status":
			s := domain.Status(v)
			if !s.Valid() {
				return f, badRequest("status must be one of: todo, in_progress, done")
			}
			f.Status = &s
		case "priority":
			p := domain.Priority(v)
			if !p.Valid() {
				return f, badRequest("priority must be one of: low, medium, high, urgent")
			}
			f.Priority = &p
		case "assigned_to":
			id, e := parseUUIDField("assigned_to", v)
			if e != nil {
				return f, e
			}
			f.AssignedTo = &id
		case "project_id":
			id, e := parseUUIDField("project_id", v)
			if e != nil {
				return f, e
			}
			f.ProjectID = &id
		case "due_after":
			t, e := time.Parse(time.RFC3339, v)
			if e != nil {
				return f, badRequest("due_after must be an RFC3339 timestamp")
			}
			f.DueAfter = &t
		case "due_before":
			t, e := time.Parse(time.RFC3339, v)
			if e != nil {
				return f, badRequest("due_before must be an RFC3339 timestamp")
			}
			f.DueBefore = &t
		case "sort_by":
			sb := domain.TaskSortField(v)
			switch sb {
			case domain.TaskSortCreatedAt, domain.TaskSortDueAt, domain.TaskSortPriority, domain.TaskSortStatus:
				f.SortBy = sb
			default:
				return f, badRequest("sort_by must be one of: created_at, due_at, priority, status")
			}
		case "sort_order":
			switch v {
			case "asc":
				f.SortDesc = false
			case "desc":
				f.SortDesc = true
			default:
				return f, badRequest("sort_order must be asc or desc")
			}
		}
	}
	return f, nil
}
