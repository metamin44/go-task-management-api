package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// UserService implements profile retrieval/update and account deletion.
// Returned users never contain the password hash.
type UserService struct {
	users domain.UserRepository
}

// NewUserService wires the user service.
func NewUserService(users domain.UserRepository) *UserService {
	return &UserService{users: users}
}

// Profile returns the current user without the password hash.
func (s *UserService) Profile(ctx context.Context, userID domain.UUID) (*domain.User, error) {
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	return u, nil
}

// UpdateProfile applies a partial update of name/email. A nil field means "no
// change"; merging happens here on the freshest read so an omitted field always
// keeps its latest committed value (no stale handler-side merge). Email is
// normalized; uniqueness is enforced by the DB expression index -> ErrConflict.
// The repository's updated_at predicate rejects concurrent modifications.
func (s *UserService) UpdateProfile(ctx context.Context, userID domain.UUID, name, email *string) (*domain.User, error) {
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if err := domain.ValidateUserName(n); err != nil {
			return nil, fmt.Errorf("%w: %s", domain.ErrInvalid, err)
		}
		u.Name = n
	}
	if email != nil {
		if err := domain.ValidateEmail(*email); err != nil {
			return nil, fmt.Errorf("%w: %s", domain.ErrInvalid, err)
		}
		u.Email = domain.NormalizeEmail(*email)
	}
	if err := s.users.Update(ctx, u); err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	return u, nil
}

// DeleteAccount hard-deletes the user. RESTRICT violations (created
// projects/tasks, memberships) surface domain.ErrReference -> 409 per design.
func (s *UserService) DeleteAccount(ctx context.Context, userID domain.UUID) error {
	return s.users.Delete(ctx, userID)
}
