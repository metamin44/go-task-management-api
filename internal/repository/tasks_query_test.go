package repository

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

func TestBuildTaskListQueryDefaults(t *testing.T) {
	query, args, err := buildTaskListQuery(domain.TaskFilter{})
	if err != nil {
		t.Fatalf("default filter: %v", err)
	}
	want := "SELECT " + taskCols + " FROM tasks ORDER BY created_at ASC, id ASC LIMIT $1 OFFSET $2"
	if query != want {
		t.Errorf("query = %q, want %q", query, want)
	}
	if len(args) != 2 || args[0] != defaultTaskLimit || args[1] != 0 {
		t.Errorf("args = %v, want [50 0]", args)
	}
}

func TestBuildTaskListQueryFilters(t *testing.T) {
	pid, uid := domain.NewUUID(), domain.NewUUID()
	status, priority := domain.StatusInProgress, domain.PriorityHigh
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	before := after.Add(48 * time.Hour)

	f := domain.TaskFilter{
		ProjectID:  &pid,
		AssignedTo: &uid,
		Status:     &status,
		Priority:   &priority,
		DueAfter:   &after,
		DueBefore:  &before,
		SortBy:     domain.TaskSortDueAt,
		SortDesc:   true,
		Limit:      25,
		Offset:     50,
	}
	query, args, err := buildTaskListQuery(f)
	if err != nil {
		t.Fatalf("full filter: %v", err)
	}
	want := "SELECT " + taskCols + " FROM tasks" +
		" WHERE project_id = $1 AND assigned_to = $2 AND status = $3 AND priority = $4 AND due_at >= $5 AND due_at <= $6" +
		" ORDER BY due_at DESC NULLS LAST, id ASC LIMIT $7 OFFSET $8"
	if query != want {
		t.Errorf("query = %q, want %q", query, want)
	}
	wantArgs := []any{pid, uid, "in_progress", "high", after, before, 25, 50}
	if len(args) != len(wantArgs) {
		t.Fatalf("args len = %d, want %d", len(args), len(wantArgs))
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Errorf("args[%d] = %v, want %v", i, args[i], wantArgs[i])
		}
	}
}

func TestBuildTaskListQuerySemanticSorts(t *testing.T) {
	cases := []struct {
		sortBy   domain.TaskSortField
		wantExpr string
	}{
		{domain.TaskSortPriority, "CASE priority WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'urgent' THEN 4 END ASC"},
		{domain.TaskSortStatus, "CASE status WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'done' THEN 3 END ASC"},
		{domain.TaskSortCreatedAt, "created_at ASC"},
		{domain.TaskSortDueAt, "due_at ASC NULLS LAST"},
	}
	for _, tc := range cases {
		t.Run(string(tc.sortBy), func(t *testing.T) {
			query, _, err := buildTaskListQuery(domain.TaskFilter{SortBy: tc.sortBy})
			if err != nil {
				t.Fatalf("sort %s: %v", tc.sortBy, err)
			}
			if !strings.Contains(query, "ORDER BY "+tc.wantExpr+", id ASC") {
				t.Errorf("query %q must order by %q", query, tc.wantExpr)
			}
		})
	}
}

func TestBuildTaskListQueryRejectsUnknownSort(t *testing.T) {
	for _, bad := range []domain.TaskSortField{"title", "id; DROP TABLE tasks", "created_at DESC--", ""} {
		if bad == "" {
			continue // empty is the documented default
		}
		_, _, err := buildTaskListQuery(domain.TaskFilter{SortBy: bad})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("sort %q must be rejected with ErrInvalid, got %v", bad, err)
		}
	}
}

func TestBuildTaskListQueryPaginationBounds(t *testing.T) {
	cases := []struct {
		name       string
		limit      int
		offset     int
		wantLimit  int
		wantOffset int
	}{
		{"zero_limit_defaults", 0, 0, 50, 0},
		{"negative_limit_defaults", -5, 0, 50, 0},
		{"over_max_capped", 5000, 0, 200, 0},
		{"negative_offset_zeroed", 10, -3, 10, 0},
		{"valid_passthrough", 25, 75, 25, 75},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, args, err := buildTaskListQuery(domain.TaskFilter{Limit: tc.limit, Offset: tc.offset})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if args[0] != tc.wantLimit || args[1] != tc.wantOffset {
				t.Errorf("limit/offset args = %v/%v, want %v/%v", args[0], args[1], tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

func TestBuildTaskListQueryValuesAlwaysParameterized(t *testing.T) {
	pid := domain.NewUUID()
	status := domain.Status("todo' OR '1'='1")
	query, args, err := buildTaskListQuery(domain.TaskFilter{ProjectID: &pid, Status: &status})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(query, "OR '1'='1") || strings.Contains(query, pid.String()) {
		t.Error("filter values must never be interpolated into SQL text")
	}
	if len(args) != 4 {
		t.Errorf("args len = %d, want 4 (project, status, limit, offset)", len(args))
	}
}
