//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

const retention = 720 * time.Hour // finalized 30-day window

// agedSpec describes a fixture row using PG-clock intervals so boundary
// assertions never depend on Go/PG clock skew. created_at is always 60 days
// ago (satisfies expires_at > created_at for every case below).
type agedSpec struct {
	name       string
	expiresAgo string // interval subtracted from now(); negative = future
	revokedAgo string // "" = never revoked (NULL)
	replacedBy *domain.UUID
	deleted    bool // expected cleanup outcome at 30-day retention
}

func retentionSpecs() []agedSpec {
	return []agedSpec{
		{"revoked_40d", "39 days", "40 days", nil, true},
		{"revoked_35d", "34 days", "35 days", nil, true},
		{"revoked_10d", "9 days", "10 days", nil, false},
		{"revoked_5d", "4 days", "5 days", nil, false},
		{"active_future", "-1 day", "", nil, false},
		{"expired_never_revoked_40d", "40 days", "", nil, true},
		{"revoked_boundary_outside", "39 days", "30 days + 1 minute", nil, true},
		{"revoked_boundary_inside", "39 days", "30 days - 1 minute", nil, false},
		{"expired_boundary_outside", "30 days + 1 minute", "", nil, true},
		{"expired_boundary_inside", "30 days - 1 minute", "", nil, false},
	}
}

func insertAged(t *testing.T, f *fixture, uid domain.UUID, s agedSpec) domain.UUID {
	t.Helper()
	id, sid := domain.NewUUID(), domain.NewUUID()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO refresh_sessions
		     (id, session_id, user_id, token_hash, expires_at, created_at, revoked_at, replaced_by)
		 VALUES ($1, $2, $3, $4,
		     now() - $5::interval,
		     now() - interval '60 days',
		     CASE WHEN $6 = '' THEN NULL ELSE now() - $6::interval END,
		     $7)`,
		id, sid, uid, tokenHash(s.name), s.expiresAgo, s.revokedAgo, s.replacedBy); err != nil {
		t.Fatalf("insert aged fixture %s: %v", s.name, err)
	}
	return id
}

func seedSpecs(t *testing.T, f *fixture) (map[string]domain.UUID, []string) {
	t.Helper()
	uid := seedUser(t, f, "user@example.com")
	ids := map[string]domain.UUID{}
	var wantDeleted []string
	for _, s := range retentionSpecs() {
		ids[s.name] = insertAged(t, f, uid, s)
		if s.deleted {
			wantDeleted = append(wantDeleted, s.name)
		}
	}
	return ids, wantDeleted
}

func assertSurvivors(t *testing.T, f *fixture, ids map[string]domain.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, s := range retentionSpecs() {
		var exists bool
		if err := f.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM refresh_sessions WHERE id = $1)", ids[s.name]).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists == s.deleted {
			t.Errorf("%s: exists=%v, expected deleted=%v", s.name, exists, s.deleted)
		}
	}
}

// T4: retention rules + ±1min boundaries, batch size 1 repeated until zero.
func TestT4RetentionBoundariesBatchSizeOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ids, wantDeleted := seedSpecs(t, f)

	var total int64
	for i := 0; i < 20; i++ { // hard guard; expected: len(wantDeleted)+1 calls
		n, err := f.sessions.DeleteExpiredBatch(ctx, retention, 1)
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		if n > 1 {
			t.Fatalf("batch size 1 must delete at most one row, got %d", n)
		}
		total += n
		if n == 0 {
			break
		}
	}
	if total != int64(len(wantDeleted)) {
		t.Errorf("deleted %d rows, want %d", total, len(wantDeleted))
	}
	assertSurvivors(t, f, ids)

	// Idempotent second run.
	n, err := f.sessions.DeleteExpiredBatch(ctx, retention, 10)
	if err != nil || n != 0 {
		t.Errorf("second run must delete nothing: n=%d err=%v", n, err)
	}
}

// T4: batch size >= 10 deletes all eligible rows in one statement.
func TestT4BatchSizeTen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ids, wantDeleted := seedSpecs(t, f)

	n, err := f.sessions.DeleteExpiredBatch(ctx, retention, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(wantDeleted)) {
		t.Errorf("single batch deleted %d, want %d", n, len(wantDeleted))
	}
	assertSurvivors(t, f, ids)

	// Invalid limit is rejected.
	if _, err := f.sessions.DeleteExpiredBatch(ctx, retention, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("limit 0 must be invalid, got %v", err)
	}
}

// T4: SET NULL on the self-FK is observable WITHIN the deleting transaction
// (the referential action is not deferred — only the replaced_by FK check is).
func TestT4SetNullWithinDeletingTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "user@example.com")

	old := insertAged(t, f, uid, agedSpec{name: "old_revoked_40d", expiresAgo: "39 days", revokedAgo: "40 days"})
	kept := insertAged(t, f, uid, agedSpec{name: "kept_revoked_5d", expiresAgo: "4 days", revokedAgo: "5 days", replacedBy: &old})

	err := f.tx.WithTx(ctx, func(ctx context.Context) error {
		n, err := f.sessions.DeleteExpiredBatch(ctx, retention, 10)
		if err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("expected the old row deleted, got %d", n)
		}
		// Same transaction: the kept row's replaced_by must already be NULL.
		rs, err := f.sessions.GetByTokenHashForUpdate(ctx, tokenHash("kept_revoked_5d"))
		if err != nil {
			return err
		}
		if rs.ReplacedBy != nil {
			t.Error("SET NULL must be visible inside the deleting transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cleanup tx: %v", err)
	}

	var exists bool
	if err := f.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM refresh_sessions WHERE id = $1)", old).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("old row must be gone after commit")
	}
	rs, err := f.sessions.GetByTokenHashForUpdate(ctx, tokenHash("kept_revoked_5d"))
	if err != nil || rs.ID != kept || rs.ReplacedBy != nil {
		t.Errorf("kept row must survive with NULL replaced_by: %v", err)
	}
}

// T4: concurrent cleanup + rotation stays consistent — no unretried
// serialization failures, at most one active row per session, and every
// replaced_by link resolves or is NULL.
func TestT4ConcurrentCleanupAndRotation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "aged@example.com")
	for i := 0; i < 8; i++ {
		insertAged(t, f, uid, agedSpec{
			name:       "aged_" + string(rune('a'+i)),
			expiresAgo: "39 days", revokedAgo: "40 days",
		})
	}
	pair1, _ := loginPair(t, f, "live@example.com")
	pair2, err := f.auth.Login(ctx, "live@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var unexpected []error
	record := func(err error) {
		if err == nil || errors.Is(err, service.ErrTokenReused) || errors.Is(err, service.ErrUnauthorized) {
			return
		}
		mu.Lock()
		unexpected = append(unexpected, err)
		mu.Unlock()
	}

	// Cleanup loop: small batches until zero.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			n, err := f.sessions.DeleteExpiredBatch(ctx, retention, 2)
			if err != nil {
				record(err)
				return
			}
			if n == 0 {
				return
			}
		}
	}()

	// Concurrent rotations: 8 presentations of token 1 + one of token 2.
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.auth.Refresh(ctx, pair1.RefreshToken)
			record(err)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := f.auth.Refresh(ctx, pair2.RefreshToken)
		record(err)
	}()
	close(start)
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("unexpected errors during concurrent cleanup+rotation: %v", unexpected)
	}
	// All aged rows cleaned.
	var agedLeft int64
	if err := f.pool.QueryRow(ctx,
		"SELECT count(*) FROM refresh_sessions WHERE user_id = $1", uid).Scan(&agedLeft); err != nil {
		t.Fatal(err)
	}
	if agedLeft != 0 {
		t.Errorf("aged rows must all be cleaned, %d left", agedLeft)
	}
	// At most one active row per session.
	var badSessions int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM (
		     SELECT session_id FROM refresh_sessions WHERE revoked_at IS NULL
		     GROUP BY session_id HAVING count(*) > 1
		 ) s`).Scan(&badSessions); err != nil {
		t.Fatal(err)
	}
	if badSessions != 0 {
		t.Errorf("sessions with >1 active row: %d", badSessions)
	}
	// Every replaced_by link resolves or is NULL (SET NULL kept it safe).
	var dangling int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM refresh_sessions rs
		 WHERE rs.replaced_by IS NOT NULL
		   AND NOT EXISTS (SELECT 1 FROM refresh_sessions x WHERE x.id = rs.replaced_by)`).Scan(&dangling); err != nil {
		t.Fatal(err)
	}
	if dangling != 0 {
		t.Errorf("dangling replaced_by links: %d", dangling)
	}
}
