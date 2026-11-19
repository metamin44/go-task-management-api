package service

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// In-memory test doubles. No database, no network, dummy credentials only.
// fakeEnv holds shared state, the operation log, failure-injection hooks,
// and snapshot/rollback transaction semantics. Five repository types
// implement the domain interfaces over the same environment.

type fakeDB struct {
	users    map[domain.UUID]domain.User
	sessions map[domain.UUID]domain.RefreshSession
	projects map[domain.UUID]domain.Project
	members  map[[2]domain.UUID]domain.ProjectMember
	tasks    map[domain.UUID]domain.Task
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		users:    map[domain.UUID]domain.User{},
		sessions: map[domain.UUID]domain.RefreshSession{},
		projects: map[domain.UUID]domain.Project{},
		members:  map[[2]domain.UUID]domain.ProjectMember{},
		tasks:    map[domain.UUID]domain.Task{},
	}
}

func (db *fakeDB) clone() *fakeDB {
	c := newFakeDB()
	for k, v := range db.users {
		c.users[k] = v
	}
	for k, v := range db.sessions {
		c.sessions[k] = v
	}
	for k, v := range db.projects {
		c.projects[k] = v
	}
	for k, v := range db.members {
		c.members[k] = v
	}
	for k, v := range db.tasks {
		c.tasks[k] = v
	}
	return c
}

type fakeEnv struct {
	db  *fakeDB
	log []string

	// Failure injection hooks (consumed once unless noted).
	commitErr      error
	addMemberErr   error
	updateRoleErr  error
	updateRoleSkip int // let N UpdateRole calls succeed before updateRoleErr fires
	revokeErr      error
	lockSerialize  int  // persistent counter: LockForUpdate fails N times
	updateConflict bool // next Update sees a concurrent version bump (F-02)
}

var _ domain.Transactor = (*fakeEnv)(nil)

func newFakeEnv() *fakeEnv { return &fakeEnv{db: newFakeDB()} }

func (e *fakeEnv) op(name string) { e.log = append(e.log, name) }

// advanceUpdatedAt mirrors the SQL GREATEST(now(), updated_at + 1µs) guarantee:
// every successful update strictly advances the version, even within one tick.
func advanceUpdatedAt(prev time.Time) time.Time {
	n := time.Now()
	if !n.After(prev) {
		n = prev.Add(time.Microsecond)
	}
	return n
}

// bumpForConflict simulates a concurrent writer committing between the
// service's read and its update (consumed once).
func (e *fakeEnv) bumpForConflict(prev time.Time) time.Time {
	if e.updateConflict {
		e.updateConflict = false
		return prev.Add(time.Microsecond)
	}
	return prev
}

func (e *fakeEnv) logIndex(name string) int {
	for i, op := range e.log {
		if op == name {
			return i
		}
	}
	return -1
}

// WithTx snapshots state, runs fn, and restores the snapshot on any failure
// (including injected commit errors) — mirroring real rollback behavior.
func (e *fakeEnv) WithTx(ctx context.Context, fn func(context.Context) error) error {
	snap := e.db.clone()
	e.op("begin")
	err := fn(ctx)
	if err == nil && e.commitErr != nil {
		err = e.commitErr
		e.commitErr = nil
	}
	if err != nil {
		e.db = snap
		e.op("rollback")
		return err
	}
	e.op("commit")
	return nil
}

// ---- users ----

type fakeUserRepo struct{ e *fakeEnv }

var _ domain.UserRepository = (*fakeUserRepo)(nil)

func (r *fakeUserRepo) Create(ctx context.Context, u *domain.User) error {
	r.e.op("users.Create")
	for _, e := range r.e.db.users {
		if strings.EqualFold(e.Email, u.Email) {
			return fmt.Errorf("users: create: %w (constraint users_email_lower_uidx)", domain.ErrConflict)
		}
	}
	cp := *u
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.e.db.users[u.ID] = cp
	u.CreatedAt, u.UpdatedAt = cp.CreatedAt, cp.UpdatedAt
	return nil
}

func (r *fakeUserRepo) GetByID(ctx context.Context, id domain.UUID) (*domain.User, error) {
	if u, ok := r.e.db.users[id]; ok {
		cp := u
		return &cp, nil
	}
	return nil, fmt.Errorf("users: get: %w", domain.ErrNotFound)
}

func (r *fakeUserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	for _, u := range r.e.db.users {
		if strings.EqualFold(u.Email, email) {
			cp := u
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("users: get by email: %w", domain.ErrNotFound)
}

func (r *fakeUserRepo) Update(ctx context.Context, u *domain.User) error {
	e, ok := r.e.db.users[u.ID]
	if !ok {
		return fmt.Errorf("users: update: %w", domain.ErrNotFound)
	}
	e.UpdatedAt = r.e.bumpForConflict(e.UpdatedAt)
	r.e.db.users[u.ID] = e
	if !u.UpdatedAt.Equal(e.UpdatedAt) {
		return fmt.Errorf("users: update: %w", domain.ErrConflict)
	}
	for id, other := range r.e.db.users {
		if id != u.ID && strings.EqualFold(other.Email, u.Email) {
			return fmt.Errorf("users: update: %w (constraint users_email_lower_uidx)", domain.ErrConflict)
		}
	}
	e.Name, e.Email, e.UpdatedAt = u.Name, u.Email, advanceUpdatedAt(e.UpdatedAt)
	r.e.db.users[u.ID] = e
	u.UpdatedAt = e.UpdatedAt
	return nil
}

func (r *fakeUserRepo) UpdatePasswordHash(ctx context.Context, id domain.UUID, hash string) error {
	r.e.op("users.UpdatePasswordHash")
	e, ok := r.e.db.users[id]
	if !ok {
		return fmt.Errorf("users: update password: %w", domain.ErrNotFound)
	}
	e.PasswordHash, e.UpdatedAt = hash, advanceUpdatedAt(e.UpdatedAt)
	r.e.db.users[id] = e
	return nil
}

func (r *fakeUserRepo) Delete(ctx context.Context, id domain.UUID) error {
	if _, ok := r.e.db.users[id]; !ok {
		return fmt.Errorf("users: delete: %w", domain.ErrNotFound)
	}
	for _, p := range r.e.db.projects {
		if p.CreatedBy == id {
			return fmt.Errorf("users: delete: %w (constraint projects_created_by_fkey)", domain.ErrReference)
		}
	}
	for _, t := range r.e.db.tasks {
		if t.CreatedBy == id {
			return fmt.Errorf("users: delete: %w (constraint tasks_created_by_fkey)", domain.ErrReference)
		}
	}
	for _, m := range r.e.db.members {
		if m.UserID == id {
			return fmt.Errorf("users: delete: %w (constraint project_members_user_id_fkey)", domain.ErrReference)
		}
	}
	delete(r.e.db.users, id)
	for sid, s := range r.e.db.sessions {
		if s.UserID == id {
			delete(r.e.db.sessions, sid)
		}
	}
	return nil
}

// ---- refresh sessions ----

type fakeSessionRepo struct{ e *fakeEnv }

var _ domain.RefreshSessionRepository = (*fakeSessionRepo)(nil)

func (r *fakeSessionRepo) Create(ctx context.Context, s *domain.RefreshSession) error {
	r.e.op("sessions.Create")
	for _, e := range r.e.db.sessions {
		if bytes.Equal(e.TokenHash, s.TokenHash) {
			return fmt.Errorf("sessions: create: %w (constraint refresh_sessions_token_hash_key)", domain.ErrConflict)
		}
		if e.SessionID == s.SessionID && e.RevokedAt == nil {
			return fmt.Errorf("sessions: create: %w (constraint refresh_sessions_one_active_per_session_uidx)", domain.ErrConflict)
		}
	}
	cp := *s
	cp.CreatedAt = time.Now()
	r.e.db.sessions[s.ID] = cp
	s.CreatedAt = cp.CreatedAt
	return nil
}

func (r *fakeSessionRepo) GetByTokenHashForUpdate(ctx context.Context, hash []byte) (*domain.RefreshSession, error) {
	r.e.op("sessions.GetByTokenHashForUpdate")
	for _, s := range r.e.db.sessions {
		if bytes.Equal(s.TokenHash, hash) {
			cp := s
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("sessions: get: %w", domain.ErrNotFound)
}

func (r *fakeSessionRepo) MarkReplaced(ctx context.Context, id, replacedBy domain.UUID) error {
	r.e.op("sessions.MarkReplaced")
	s, ok := r.e.db.sessions[id]
	if !ok || s.RevokedAt != nil {
		return fmt.Errorf("sessions: mark replaced: %w", domain.ErrNotFound)
	}
	now := time.Now()
	s.RevokedAt = &now
	s.ReplacedBy = &replacedBy
	r.e.db.sessions[id] = s
	return nil
}

func (r *fakeSessionRepo) RevokeSession(ctx context.Context, sessionID domain.UUID) (int64, error) {
	r.e.op("sessions.RevokeSession")
	var n int64
	now := time.Now()
	for id, s := range r.e.db.sessions {
		if s.SessionID == sessionID && s.RevokedAt == nil {
			s.RevokedAt = &now
			r.e.db.sessions[id] = s
			n++
		}
	}
	return n, nil
}

func (r *fakeSessionRepo) RevokeUserSessions(ctx context.Context, userID domain.UUID) (int64, error) {
	r.e.op("sessions.RevokeUserSessions")
	if r.e.revokeErr != nil {
		err := r.e.revokeErr
		r.e.revokeErr = nil
		return 0, err
	}
	var n int64
	now := time.Now()
	for id, s := range r.e.db.sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			s.RevokedAt = &now
			r.e.db.sessions[id] = s
			n++
		}
	}
	return n, nil
}

func (r *fakeSessionRepo) DeleteExpiredBatch(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	if limit < 1 {
		return 0, fmt.Errorf("sessions: cleanup: %w", domain.ErrInvalid)
	}
	cutoff := time.Now().Add(-retention)
	var n int64
	for id, s := range r.e.db.sessions {
		if n >= int64(limit) {
			break
		}
		if (s.RevokedAt != nil && s.RevokedAt.Before(cutoff)) ||
			(s.RevokedAt == nil && s.ExpiresAt.Before(cutoff)) {
			delete(r.e.db.sessions, id)
			n++
		}
	}
	return n, nil
}

// ---- projects ----

type fakeProjectRepo struct{ e *fakeEnv }

var _ domain.ProjectRepository = (*fakeProjectRepo)(nil)

func (r *fakeProjectRepo) Create(ctx context.Context, p *domain.Project) error {
	r.e.op("projects.Create")
	cp := *p
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.e.db.projects[p.ID] = cp
	p.CreatedAt, p.UpdatedAt = cp.CreatedAt, cp.UpdatedAt
	return nil
}

func (r *fakeProjectRepo) GetByID(ctx context.Context, id domain.UUID) (*domain.Project, error) {
	if p, ok := r.e.db.projects[id]; ok {
		cp := p
		return &cp, nil
	}
	return nil, fmt.Errorf("projects: get: %w", domain.ErrNotFound)
}

func (r *fakeProjectRepo) LockForUpdate(ctx context.Context, id domain.UUID) error {
	r.e.op("projects.LockForUpdate")
	if r.e.lockSerialize > 0 {
		r.e.lockSerialize--
		return fmt.Errorf("projects: lock: %w", domain.ErrSerialization)
	}
	if _, ok := r.e.db.projects[id]; !ok {
		return fmt.Errorf("projects: lock: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *fakeProjectRepo) ListByMember(ctx context.Context, userID domain.UUID) ([]domain.Project, error) {
	out := make([]domain.Project, 0)
	for _, m := range r.e.db.members {
		if m.UserID == userID {
			if p, ok := r.e.db.projects[m.ProjectID]; ok {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func (r *fakeProjectRepo) Update(ctx context.Context, p *domain.Project) error {
	e, ok := r.e.db.projects[p.ID]
	if !ok {
		return fmt.Errorf("projects: update: %w", domain.ErrNotFound)
	}
	e.UpdatedAt = r.e.bumpForConflict(e.UpdatedAt)
	r.e.db.projects[p.ID] = e
	if !p.UpdatedAt.Equal(e.UpdatedAt) {
		return fmt.Errorf("projects: update: %w", domain.ErrConflict)
	}
	e.Name, e.Description, e.UpdatedAt = p.Name, p.Description, advanceUpdatedAt(e.UpdatedAt)
	r.e.db.projects[p.ID] = e
	p.UpdatedAt = e.UpdatedAt
	return nil
}

func (r *fakeProjectRepo) Delete(ctx context.Context, id domain.UUID) error {
	if _, ok := r.e.db.projects[id]; !ok {
		return fmt.Errorf("projects: delete: %w", domain.ErrNotFound)
	}
	delete(r.e.db.projects, id)
	for k, m := range r.e.db.members {
		if m.ProjectID == id {
			delete(r.e.db.members, k)
		}
	}
	for tid, t := range r.e.db.tasks {
		if t.ProjectID == id {
			delete(r.e.db.tasks, tid)
		}
	}
	return nil
}

// ---- project members ----

type fakeMemberRepo struct{ e *fakeEnv }

var _ domain.ProjectMemberRepository = (*fakeMemberRepo)(nil)

func (r *fakeMemberRepo) Add(ctx context.Context, m *domain.ProjectMember) error {
	r.e.op("members.Add")
	if r.e.addMemberErr != nil {
		err := r.e.addMemberErr
		r.e.addMemberErr = nil
		return err
	}
	key := [2]domain.UUID{m.ProjectID, m.UserID}
	if _, exists := r.e.db.members[key]; exists {
		return fmt.Errorf("members: add: %w (constraint project_members_pkey)", domain.ErrConflict)
	}
	if m.Role == domain.RoleOwner {
		for _, e := range r.e.db.members {
			if e.ProjectID == m.ProjectID && e.Role == domain.RoleOwner {
				return fmt.Errorf("members: add: %w (constraint project_members_one_owner_per_project_uidx)", domain.ErrConflict)
			}
		}
	}
	cp := *m
	cp.JoinedAt = time.Now()
	r.e.db.members[key] = cp
	m.JoinedAt = cp.JoinedAt
	return nil
}

func (r *fakeMemberRepo) Get(ctx context.Context, projectID, userID domain.UUID) (*domain.ProjectMember, error) {
	if m, ok := r.e.db.members[[2]domain.UUID{projectID, userID}]; ok {
		cp := m
		return &cp, nil
	}
	return nil, fmt.Errorf("members: get: %w", domain.ErrNotFound)
}

func (r *fakeMemberRepo) GetOwner(ctx context.Context, projectID domain.UUID) (*domain.ProjectMember, error) {
	for _, m := range r.e.db.members {
		if m.ProjectID == projectID && m.Role == domain.RoleOwner {
			cp := m
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("members: get owner: %w", domain.ErrNotFound)
}

func (r *fakeMemberRepo) ListByProject(ctx context.Context, projectID domain.UUID) ([]domain.ProjectMember, error) {
	out := make([]domain.ProjectMember, 0)
	for _, m := range r.e.db.members {
		if m.ProjectID == projectID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (r *fakeMemberRepo) UpdateRole(ctx context.Context, projectID, userID domain.UUID, role domain.Role) error {
	r.e.op("members.UpdateRole")
	if r.e.updateRoleErr != nil {
		if r.e.updateRoleSkip > 0 {
			r.e.updateRoleSkip--
		} else {
			err := r.e.updateRoleErr
			r.e.updateRoleErr = nil
			return err
		}
	}
	key := [2]domain.UUID{projectID, userID}
	m, ok := r.e.db.members[key]
	if !ok {
		return fmt.Errorf("members: update role: %w", domain.ErrNotFound)
	}
	if role == domain.RoleOwner {
		for k, e := range r.e.db.members {
			if k != key && e.ProjectID == projectID && e.Role == domain.RoleOwner {
				return fmt.Errorf("members: update role: %w (constraint project_members_one_owner_per_project_uidx)", domain.ErrConflict)
			}
		}
	}
	m.Role = role
	r.e.db.members[key] = m
	return nil
}

func (r *fakeMemberRepo) Remove(ctx context.Context, projectID, userID domain.UUID) error {
	r.e.op("members.Remove")
	key := [2]domain.UUID{projectID, userID}
	if _, ok := r.e.db.members[key]; !ok {
		return fmt.Errorf("members: remove: %w", domain.ErrNotFound)
	}
	for _, t := range r.e.db.tasks {
		if t.ProjectID == projectID && t.AssignedTo != nil && *t.AssignedTo == userID {
			return fmt.Errorf("members: remove: %w (constraint tasks_project_assignee_fkey)", domain.ErrReference)
		}
	}
	delete(r.e.db.members, key)
	return nil
}

// ---- tasks ----

type fakeTaskRepo struct{ e *fakeEnv }

var _ domain.TaskRepository = (*fakeTaskRepo)(nil)

func (r *fakeTaskRepo) checkAssignee(projectID domain.UUID, assignedTo *domain.UUID) error {
	if assignedTo == nil {
		return nil
	}
	if _, ok := r.e.db.members[[2]domain.UUID{projectID, *assignedTo}]; !ok {
		return fmt.Errorf("tasks: assignee: %w (constraint tasks_project_assignee_fkey)", domain.ErrReference)
	}
	return nil
}

func (r *fakeTaskRepo) Create(ctx context.Context, t *domain.Task) error {
	r.e.op("tasks.Create")
	if err := r.checkAssignee(t.ProjectID, t.AssignedTo); err != nil {
		return err
	}
	cp := *t
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.e.db.tasks[t.ID] = cp
	t.CreatedAt, t.UpdatedAt = cp.CreatedAt, cp.UpdatedAt
	return nil
}

func (r *fakeTaskRepo) GetByID(ctx context.Context, id domain.UUID) (*domain.Task, error) {
	if t, ok := r.e.db.tasks[id]; ok {
		cp := t
		return &cp, nil
	}
	return nil, fmt.Errorf("tasks: get: %w", domain.ErrNotFound)
}

func (r *fakeTaskRepo) Update(ctx context.Context, t *domain.Task) error {
	r.e.op("tasks.Update")
	e, ok := r.e.db.tasks[t.ID]
	if !ok {
		return fmt.Errorf("tasks: update: %w", domain.ErrNotFound)
	}
	e.UpdatedAt = r.e.bumpForConflict(e.UpdatedAt)
	r.e.db.tasks[t.ID] = e
	if !t.UpdatedAt.Equal(e.UpdatedAt) {
		return fmt.Errorf("tasks: update: %w", domain.ErrConflict)
	}
	if err := r.checkAssignee(t.ProjectID, t.AssignedTo); err != nil {
		return err
	}
	e.AssignedTo, e.Title, e.Description = t.AssignedTo, t.Title, t.Description
	e.Status, e.Priority, e.DueAt = t.Status, t.Priority, t.DueAt
	e.UpdatedAt = advanceUpdatedAt(e.UpdatedAt)
	r.e.db.tasks[t.ID] = e
	t.UpdatedAt = e.UpdatedAt
	return nil
}

func (r *fakeTaskRepo) Delete(ctx context.Context, id domain.UUID) error {
	if _, ok := r.e.db.tasks[id]; !ok {
		return fmt.Errorf("tasks: delete: %w", domain.ErrNotFound)
	}
	delete(r.e.db.tasks, id)
	return nil
}

func (r *fakeTaskRepo) List(ctx context.Context, filter domain.TaskFilter) ([]domain.Task, error) {
	matched := make([]domain.Task, 0)
	for _, t := range r.e.db.tasks {
		if filter.ProjectID != nil && t.ProjectID != *filter.ProjectID {
			continue
		}
		if filter.AssignedTo != nil && (t.AssignedTo == nil || *t.AssignedTo != *filter.AssignedTo) {
			continue
		}
		if filter.Status != nil && t.Status != *filter.Status {
			continue
		}
		if filter.Priority != nil && t.Priority != *filter.Priority {
			continue
		}
		if filter.DueAfter != nil && (t.DueAt == nil || t.DueAt.Before(*filter.DueAfter)) {
			continue
		}
		if filter.DueBefore != nil && (t.DueAt == nil || t.DueAt.After(*filter.DueBefore)) {
			continue
		}
		matched = append(matched, t)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].ID.String() < matched[j].ID.String()
		}
		return matched[i].CreatedAt.Before(matched[j].CreatedAt)
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(matched) {
		return []domain.Task{}, nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], nil
}

func (r *fakeTaskRepo) UnassignMemberTasks(ctx context.Context, projectID, userID domain.UUID) (int64, error) {
	var n int64
	for id, t := range r.e.db.tasks {
		if t.ProjectID == projectID && t.AssignedTo != nil && *t.AssignedTo == userID {
			t.AssignedTo = nil
			t.UpdatedAt = advanceUpdatedAt(t.UpdatedAt)
			r.e.db.tasks[id] = t
			n++
		}
	}
	return n, nil
}

// ---- aggregate fixture handle ----

// fakeStore bundles the shared environment (state, log, hooks, Transactor)
// with the five repositories. Tests access state via f.db and repos via
// f.users / f.sessions / f.projects / f.members / f.tasks.
type fakeStore struct {
	*fakeEnv
	users    fakeUserRepo
	sessions fakeSessionRepo
	projects fakeProjectRepo
	members  fakeMemberRepo
	tasks    fakeTaskRepo
}

func newFakeStore() *fakeStore {
	e := newFakeEnv()
	return &fakeStore{
		fakeEnv:  e,
		users:    fakeUserRepo{e},
		sessions: fakeSessionRepo{e},
		projects: fakeProjectRepo{e},
		members:  fakeMemberRepo{e},
		tasks:    fakeTaskRepo{e},
	}
}

// ---- other doubles ----

// fakeHasher is a deterministic stand-in; real argon2id is tested separately.
type fakeHasher struct{}

func (fakeHasher) Hash(pw string) (string, error) { return "encoded:" + pw, nil }
func (fakeHasher) Verify(encoded, pw string) (bool, error) {
	return encoded == "encoded:"+pw, nil
}

// fakeTokens issues prefixed opaque stand-ins for access tokens.
type fakeTokens struct{}

func (fakeTokens) Issue(userID domain.UUID, now time.Time) (string, error) {
	return "access." + userID.String(), nil
}

func (fakeTokens) Validate(token string, now time.Time) (*TokenClaims, error) {
	if !strings.HasPrefix(token, "access.") {
		return nil, fmt.Errorf("access token invalid: %w", ErrUnauthorized)
	}
	uid, err := domain.ParseUUID(strings.TrimPrefix(token, "access."))
	if err != nil {
		return nil, fmt.Errorf("access token invalid: %w", ErrUnauthorized)
	}
	return &TokenClaims{UserID: uid, ExpiresAt: now.Add(15 * time.Minute)}, nil
}

// ---- seeding helpers ----

func seedUser(f *fakeStore, email string) domain.UUID {
	id := domain.NewUUID()
	f.db.users[id] = domain.User{ID: id, Name: "Test User", Email: email, PasswordHash: "encoded:password123"}
	return id
}

func seedProject(f *fakeStore, ownerID domain.UUID) domain.UUID {
	pid := domain.NewUUID()
	f.db.projects[pid] = domain.Project{ID: pid, Name: "Project", CreatedBy: ownerID}
	f.db.members[[2]domain.UUID{pid, ownerID}] = domain.ProjectMember{ProjectID: pid, UserID: ownerID, Role: domain.RoleOwner}
	return pid
}

func seedMember(f *fakeStore, pid, uid domain.UUID, role domain.Role) {
	f.db.members[[2]domain.UUID{pid, uid}] = domain.ProjectMember{ProjectID: pid, UserID: uid, Role: role}
}

func newTestAuthService(f *fakeStore) *AuthService {
	s, err := NewAuthService(&f.users, &f.sessions, f, fakeHasher{}, fakeTokens{}, 30*24*time.Hour)
	if err != nil {
		panic(err)
	}
	return s
}
