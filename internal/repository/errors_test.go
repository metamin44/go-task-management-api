package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

func TestMapError(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantSentinel error
		wantInMsg    string
	}{
		{"no_rows", pgx.ErrNoRows, domain.ErrNotFound, "not found"},
		{"wrapped_no_rows", fmt.Errorf("scan: %w", pgx.ErrNoRows), domain.ErrNotFound, ""},
		{"unique", &pgconn.PgError{Code: pgerrcode.UniqueViolation, ConstraintName: "users_email_lower_uidx"}, domain.ErrConflict, "users_email_lower_uidx"},
		{"fk", &pgconn.PgError{Code: pgerrcode.ForeignKeyViolation, ConstraintName: "tasks_project_assignee_fkey"}, domain.ErrReference, "tasks_project_assignee_fkey"},
		{"check", &pgconn.PgError{Code: pgerrcode.CheckViolation}, domain.ErrInvalid, ""},
		{"not_null", &pgconn.PgError{Code: pgerrcode.NotNullViolation}, domain.ErrInvalid, ""},
		{"bad_type", &pgconn.PgError{Code: pgerrcode.InvalidTextRepresentation}, domain.ErrInvalid, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapError("op", tc.err)
			if got == nil {
				t.Fatal("expected non-nil error")
			}
			if !errors.Is(got, tc.wantSentinel) {
				t.Errorf("error %v must wrap sentinel %v", got, tc.wantSentinel)
			}
			if tc.wantInMsg != "" && !strings.Contains(got.Error(), tc.wantInMsg) {
				t.Errorf("error must mention %q", tc.wantInMsg)
			}
			if !strings.Contains(got.Error(), "op") {
				t.Error("error must include the operation context")
			}
		})
	}
}

func TestMapErrorNil(t *testing.T) {
	if got := mapError("op", nil); got != nil {
		t.Errorf("mapError(nil) = %v, want nil", got)
	}
}

func TestMapErrorNeverLeaksRawDetails(t *testing.T) {
	raw := &pgconn.PgError{
		Code:           "XX000", // internal_error: unmapped
		Message:        "sensitive-sql-text SELECT password_hash FROM users",
		Detail:         "sensitive-detail",
		Hint:           "sensitive-hint",
		Where:          "sensitive-where",
		ConstraintName: "some_constraint",
	}
	got := mapError("users: create", raw)
	if got == nil {
		t.Fatal("expected error")
	}
	msg := got.Error()
	for _, leak := range []string{"sensitive-sql-text", "password_hash", "sensitive-detail", "sensitive-hint", "sensitive-where"} {
		if strings.Contains(msg, leak) {
			t.Error("mapped error must not expose raw driver details")
		}
	}
	if errors.Is(got, domain.ErrConflict) || errors.Is(got, domain.ErrReference) {
		t.Error("unmapped code must not be classified as conflict/reference")
	}
}

func TestMapErrorPreservesContextErrors(t *testing.T) {
	if got := mapError("op", context.Canceled); !errors.Is(got, context.Canceled) {
		t.Error("context.Canceled must pass through")
	}
	if got := mapError("op", context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded must pass through")
	}
}

func TestRequireRows(t *testing.T) {
	// pgconn.NewCommandTag builds tags without a database connection.
	if err := requireRows("op", pgconn.NewCommandTag("UPDATE 1")); err != nil {
		t.Errorf("one affected row must succeed: %v", err)
	}
	err := requireRows("op", pgconn.NewCommandTag("UPDATE 0"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("zero affected rows must map to ErrNotFound, got %v", err)
	}
}
