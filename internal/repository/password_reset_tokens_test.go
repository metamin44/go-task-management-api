package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// recordingQuerier captures the SQL and arguments a repository method issues,
// without any database. It embeds pgx.Tx so it can be carried in the context
// (querierFrom) exactly like a real transaction; only Exec/QueryRow are used.
type recordingQuerier struct {
	pgx.Tx
	sql  string
	args []any
	rows int64
}

func (q *recordingQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.sql, q.args = sql, args
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", q.rows)), nil
}

func (q *recordingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.sql, q.args = sql, args
	return nil, nil
}

func (q *recordingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.sql, q.args = sql, args
	return recordingRow{}
}

type recordingRow struct{}

func (recordingRow) Scan(dest ...any) error {
	for _, d := range dest {
		if t, ok := d.(*time.Time); ok {
			*t = time.Now()
		}
	}
	return nil
}

func resetCtx(q *recordingQuerier) context.Context {
	return context.WithValue(context.Background(), txKey{}, pgx.Tx(q))
}

func TestPasswordResetCreateSQL(t *testing.T) {
	q := &recordingQuerier{}
	r := &PasswordResetTokenRepository{}
	hash := make([]byte, 32)
	tok := &domain.PasswordResetToken{ID: domain.NewUUID(), UserID: domain.NewUUID(), TokenHash: hash, ExpiresAt: time.Now().Add(15 * time.Minute)}
	if err := r.Create(resetCtx(q), tok); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(q.sql, "INSERT INTO password_reset_tokens") ||
		!strings.Contains(q.sql, "$1, $2, $3, $4") ||
		!strings.Contains(q.sql, "RETURNING created_at") {
		t.Errorf("unexpected create SQL: %s", q.sql)
	}
	if len(q.args) != 4 {
		t.Errorf("create must be fully parameterized with 4 args, got %d", len(q.args))
	}
	if tok.CreatedAt.IsZero() {
		t.Error("created_at must be scanned back from RETURNING")
	}
}

func TestPasswordResetGetForUpdateSQL(t *testing.T) {
	q := &recordingQuerier{}
	r := &PasswordResetTokenRepository{}
	if _, err := r.GetByTokenHashForUpdate(resetCtx(q), make([]byte, 32)); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(q.sql, "FOR UPDATE") || !strings.Contains(q.sql, "WHERE token_hash = $1") {
		t.Errorf("get must lock the row by hash parameter: %s", q.sql)
	}
	if len(q.args) != 1 {
		t.Errorf("get must take exactly one parameter, got %d", len(q.args))
	}
}

func TestPasswordResetMarkConsumedSQL(t *testing.T) {
	r := &PasswordResetTokenRepository{}
	t.Run("single_use_predicate", func(t *testing.T) {
		q := &recordingQuerier{rows: 1}
		if err := r.MarkConsumed(resetCtx(q), domain.NewUUID()); err != nil {
			t.Fatalf("mark consumed: %v", err)
		}
		if !strings.Contains(q.sql, "SET consumed_at = now()") ||
			!strings.Contains(q.sql, "WHERE id = $1 AND consumed_at IS NULL") {
			t.Errorf("consumption must be single-use via the consumed_at predicate: %s", q.sql)
		}
	})
	t.Run("already_consumed_is_not_found", func(t *testing.T) {
		q := &recordingQuerier{rows: 0}
		err := r.MarkConsumed(resetCtx(q), domain.NewUUID())
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("zero rows must map to ErrNotFound, got %v", err)
		}
	})
}

func TestPasswordResetDeleteUnconsumedByUserSQL(t *testing.T) {
	q := &recordingQuerier{rows: 2}
	r := &PasswordResetTokenRepository{}
	n, err := r.DeleteUnconsumedByUser(resetCtx(q), domain.NewUUID())
	if err != nil || n != 2 {
		t.Fatalf("delete unconsumed: n=%d err=%v", n, err)
	}
	if !strings.Contains(q.sql, "DELETE FROM password_reset_tokens") ||
		!strings.Contains(q.sql, "WHERE user_id = $1 AND consumed_at IS NULL") {
		t.Errorf("must delete only unconsumed rows by user parameter: %s", q.sql)
	}
}
