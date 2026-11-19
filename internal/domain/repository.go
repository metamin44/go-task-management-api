package domain

import (
	"context"
	"time"
)

// Repository interfaces are owned by the domain (dependency inversion);
// internal/repository implements them with pgx/v5. Methods are tx-aware:
// when called inside Transactor.WithTx they participate in that transaction.
// Business authorization belongs to the service layer, not repositories.

// UserRepository covers users MVP operations.
type UserRepository interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id UUID) (*User, error)
	// GetByEmail matches case-insensitively via lower(email).
	GetByEmail(ctx context.Context, email string) (*User, error)
	// Update persists Name and Email (immutable: id, password_hash, created_at).
	Update(ctx context.Context, u *User) error
	UpdatePasswordHash(ctx context.Context, id UUID, passwordHash string) error
	Delete(ctx context.Context, id UUID) error
}

// RefreshSessionRepository covers token issuance, rotation support,
// revocation, and cleanup. Rotation orchestration (locking order, reuse
// detection) lives in the service layer inside a transaction.
type RefreshSessionRepository interface {
	// Create inserts a new active token row (revoked_at/replaced_by NULL).
	Create(ctx context.Context, s *RefreshSession) error
	// GetByTokenHashForUpdate selects the row with FOR UPDATE; must run
	// inside a transaction to serialize concurrent rotations.
	GetByTokenHashForUpdate(ctx context.Context, tokenHash []byte) (*RefreshSession, error)
	// MarkReplaced revokes the old token and links its replacement
	// (rotation step; run before inserting the new row, deferred FK).
	MarkReplaced(ctx context.Context, id, replacedBy UUID) error
	// RevokeSession revokes all active tokens of one session (logout,
	// reuse detection). Returns the number of rows revoked.
	RevokeSession(ctx context.Context, sessionID UUID) (int64, error)
	// RevokeUserSessions revokes all active tokens of a user (logout-all).
	RevokeUserSessions(ctx context.Context, userID UUID) (int64, error)
	// DeleteExpiredBatch deletes up to limit rows past the retention window
	// (revoked_at or, when never revoked, expires_at older than now-retention),
	// ordered by COALESCE(revoked_at, expires_at), id. Returns rows deleted.
	DeleteExpiredBatch(ctx context.Context, retention time.Duration, limit int) (int64, error)
}

// ProjectRepository covers project CRUD and the per-project mutex used to
// serialize membership mutations.
type ProjectRepository interface {
	Create(ctx context.Context, p *Project) error
	GetByID(ctx context.Context, id UUID) (*Project, error)
	// LockForUpdate takes a row lock (SELECT ... FOR UPDATE) inside a
	// transaction; returns ErrNotFound if the project does not exist.
	LockForUpdate(ctx context.Context, id UUID) error
	// ListByMember lists projects where userID is a member.
	ListByMember(ctx context.Context, userID UUID) ([]Project, error)
	// Update persists Name and Description (immutable: id, created_by).
	Update(ctx context.Context, p *Project) error
	Delete(ctx context.Context, id UUID) error
}

// ProjectMemberRepository covers membership queries and mutations.
// Ownership transfer is orchestrated by the service (lock project, demote
// old owner, promote new owner) using UpdateRole inside one transaction.
type ProjectMemberRepository interface {
	Add(ctx context.Context, m *ProjectMember) error
	Get(ctx context.Context, projectID, userID UUID) (*ProjectMember, error)
	GetOwner(ctx context.Context, projectID UUID) (*ProjectMember, error)
	ListByProject(ctx context.Context, projectID UUID) ([]ProjectMember, error)
	UpdateRole(ctx context.Context, projectID, userID UUID, role Role) error
	Remove(ctx context.Context, projectID, userID UUID) error
}

// TaskRepository covers task CRUD, filtered/paginated listing, and bulk
// unassignment required before member removal.
type TaskRepository interface {
	Create(ctx context.Context, t *Task) error
	GetByID(ctx context.Context, id UUID) (*Task, error)
	// Update persists mutable fields: AssignedTo, Title, Description,
	// Status, Priority, DueAt (immutable: id, project_id, created_by).
	Update(ctx context.Context, t *Task) error
	Delete(ctx context.Context, id UUID) error
	List(ctx context.Context, f TaskFilter) ([]Task, error)
	// UnassignMemberTasks sets assigned_to=NULL for all tasks assigned to
	// userID within projectID. Returns rows updated.
	UnassignMemberTasks(ctx context.Context, projectID, userID UUID) (int64, error)
}

// Transactor runs fn inside a single READ COMMITTED transaction. The
// transaction is carried in the context passed to fn, so repository calls
// made with that context join it. Rollback on error or panic; commit otherwise.
type Transactor interface {
	WithTx(ctx context.Context, fn func(context.Context) error) error
}
