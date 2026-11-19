package domain

import "time"

// Role is a project membership role (DB CHECK constraint).
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Valid reports whether r is an allowed role.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember:
		return true
	}
	return false
}

// ProjectMember mirrors the project_members table (composite PK).
type ProjectMember struct {
	ProjectID UUID
	UserID    UUID
	Role      Role
	JoinedAt  time.Time
}
