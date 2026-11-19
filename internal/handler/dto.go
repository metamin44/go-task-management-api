package handler

import (
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// Response DTOs are explicit whitelists of fields: domain structs are never
// serialized directly, so sensitive fields (e.g. password hashes) cannot leak
// even if the domain grows.

type statusResponse struct {
	Status string `json:"status"`
}

type userResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toUserResponse(u *domain.User) userResponse {
	return userResponse{ID: u.ID.String(), Name: u.Name, Email: u.Email, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

type tokenPairResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func toTokenPairResponse(p *service.TokenPair) tokenPairResponse {
	return tokenPairResponse{AccessToken: p.AccessToken, RefreshToken: p.RefreshToken, TokenType: "Bearer"}
}

type projectResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toProjectResponse(p *domain.Project) projectResponse {
	return projectResponse{
		ID: p.ID.String(), Name: p.Name, Description: p.Description,
		CreatedBy: p.CreatedBy.String(), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func toProjectResponses(ps []domain.Project) []projectResponse {
	out := make([]projectResponse, 0, len(ps))
	for i := range ps {
		out = append(out, toProjectResponse(&ps[i]))
	}
	return out
}

type memberResponse struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	JoinedAt  time.Time `json:"joined_at"`
}

func toMemberResponse(m *domain.ProjectMember) memberResponse {
	return memberResponse{ProjectID: m.ProjectID.String(), UserID: m.UserID.String(), Role: string(m.Role), JoinedAt: m.JoinedAt}
}

func toMemberResponses(ms []domain.ProjectMember) []memberResponse {
	out := make([]memberResponse, 0, len(ms))
	for i := range ms {
		out = append(out, toMemberResponse(&ms[i]))
	}
	return out
}

type taskResponse struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	CreatedBy   string     `json:"created_by"`
	AssignedTo  *string    `json:"assigned_to"`
	Title       string     `json:"title"`
	Description *string    `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	DueAt       *time.Time `json:"due_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toTaskResponse(t *domain.Task) taskResponse {
	var assigned *string
	if t.AssignedTo != nil {
		s := t.AssignedTo.String()
		assigned = &s
	}
	return taskResponse{
		ID: t.ID.String(), ProjectID: t.ProjectID.String(), CreatedBy: t.CreatedBy.String(),
		AssignedTo: assigned, Title: t.Title, Description: t.Description,
		Status: string(t.Status), Priority: string(t.Priority), DueAt: t.DueAt,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func toTaskResponses(ts []domain.Task) []taskResponse {
	out := make([]taskResponse, 0, len(ts))
	for i := range ts {
		out = append(out, toTaskResponse(&ts[i]))
	}
	return out
}

// listResponse wraps collections; task lists additionally echo the applied
// pagination so clients can page deterministically.
type listResponse struct {
	Data   any  `json:"data"`
	Limit  *int `json:"limit,omitempty"`
	Offset *int `json:"offset,omitempty"`
}

type logoutAllResponse struct {
	RevokedSessions int64 `json:"revoked_sessions"`
}
