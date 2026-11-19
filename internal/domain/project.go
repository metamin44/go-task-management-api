package domain

import "time"

// Project mirrors the projects table. CreatedBy is historical attribution
// and is never rewritten after creation.
type Project struct {
	ID          UUID
	Name        string
	Description *string
	CreatedBy   UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaxProjectNameLen matches projects.name varchar(150).
const MaxProjectNameLen = 150

// ValidateProjectName enforces the projects.name rules.
func ValidateProjectName(name string) error {
	return validateText("project name", name, MaxProjectNameLen)
}
