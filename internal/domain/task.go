package domain

import "time"

// Status is a task status (DB CHECK constraint).
type Status string

const (
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
)

// Valid reports whether s is an allowed status.
func (s Status) Valid() bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusDone:
		return true
	}
	return false
}

// Priority is a task priority (DB CHECK constraint).
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// Valid reports whether p is an allowed priority.
func (p Priority) Valid() bool {
	switch p {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}

// Task mirrors the tasks table. AssignedTo must reference a member of the
// same project (enforced by the composite FK); nil means unassigned.
// ProjectID and CreatedBy are immutable after creation.
type Task struct {
	ID          UUID
	ProjectID   UUID
	CreatedBy   UUID
	AssignedTo  *UUID
	Title       string
	Description *string
	Status      Status
	Priority    Priority
	DueAt       *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaxTaskTitleLen matches tasks.title varchar(200).
const MaxTaskTitleLen = 200

// ValidateTaskTitle enforces the tasks.title rules.
func ValidateTaskTitle(title string) error { return validateText("title", title, MaxTaskTitleLen) }

// TaskSortField is a whitelisted sort column for task listing.
type TaskSortField string

const (
	TaskSortCreatedAt TaskSortField = "created_at"
	TaskSortDueAt     TaskSortField = "due_at"
	TaskSortPriority  TaskSortField = "priority"
	TaskSortStatus    TaskSortField = "status"
)

// TaskFilter describes task listing: optional filters plus pagination and
// sorting. Zero SortBy defaults to created_at ascending; Limit <= 0 defaults
// to 50 and is capped at 200; negative Offset is treated as 0.
type TaskFilter struct {
	ProjectID  *UUID
	AssignedTo *UUID
	Status     *Status
	Priority   *Priority
	DueAfter   *time.Time // due_at >= DueAfter
	DueBefore  *time.Time // due_at <= DueBefore
	SortBy     TaskSortField
	SortDesc   bool
	Limit      int
	Offset     int
}
