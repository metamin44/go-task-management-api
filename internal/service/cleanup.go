package service

import (
	"context"
	"log/slog"
	"time"
)

// SessionCleaner is the narrow, consumer-defined dependency for refresh-session
// cleanup. It is satisfied by domain.RefreshSessionRepository (and the real
// repository); the worker reuses the existing batched CTE delete without
// duplicating its SQL or changing its semantics.
type SessionCleaner interface {
	DeleteExpiredBatch(ctx context.Context, retention time.Duration, limit int) (int64, error)
}

// Cleanup policy constants. Retention and batch size match the finalized design
// (PROJECT_CONTEXT.md §6): 30-day retention, 500-row batches, each batch its own
// transaction, repeated until a batch affects fewer rows than the limit.
const (
	// DefaultCleanupInterval is the conservative default tick (off-peak friendly).
	DefaultCleanupInterval = time.Hour
	cleanupRetention       = 30 * 24 * time.Hour // 30-day retention policy
	cleanupBatchLimit      = 500                 // documented CTE batch size
	// cleanupMaxBatches bounds the work done in a single tick so a large backlog
	// cannot monopolize the worker or the pool; any remainder is handled on the
	// next tick. This is a safety bound, not a change to the drain-until-empty
	// semantics under normal load.
	cleanupMaxBatches = 200
)

// CleanupWorker periodically deletes expired/revoked refresh-session rows past
// the retention window. Run executes batches sequentially in a single goroutine,
// so executions never overlap within an instance. (Multi-replica deployments
// need external coordination, e.g. an advisory lock or a single scheduled runner;
// the in-process worker guarantees no overlap only per instance.)
type CleanupWorker struct {
	cleaner    SessionCleaner
	interval   time.Duration
	retention  time.Duration
	batchLimit int
	maxBatches int
	log        *slog.Logger
}

// NewCleanupWorker builds the worker. A non-positive interval falls back to the
// conservative default; a nil logger uses slog.Default. Retention, batch limit,
// and the per-tick batch bound are fixed by the finalized design.
func NewCleanupWorker(cleaner SessionCleaner, interval time.Duration, log *slog.Logger) *CleanupWorker {
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &CleanupWorker{
		cleaner:    cleaner,
		interval:   interval,
		retention:  cleanupRetention,
		batchLimit: cleanupBatchLimit,
		maxBatches: cleanupMaxBatches,
		log:        log,
	}
}

// Run blocks until ctx is cancelled, running one cleanup pass per tick. Because
// each pass runs to completion before the next tick is handled (and time.Ticker
// drops ticks while the receiver is busy), passes never overlap. Cancellation
// stops the loop promptly, including mid-pass. Errors are logged with secret-safe
// messages and the worker waits for the next tick — there is no tight retry loop.
func (w *CleanupWorker) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

// runOnce drains expired/revoked rows in bounded batches. Each DeleteExpiredBatch
// call is its own transaction (autocommit outside WithTx). It stops when a batch
// returns fewer rows than the limit (backlog drained), when the per-tick batch
// bound is reached, on the first error, or on context cancellation.
func (w *CleanupWorker) runOnce(ctx context.Context) {
	var total int64
	for i := 0; i < w.maxBatches; i++ {
		if ctx.Err() != nil {
			return
		}
		n, err := w.cleaner.DeleteExpiredBatch(ctx, w.retention, w.batchLimit)
		if err != nil {
			// Repository errors are static/name-only (secret-safe); never log
			// credentials, hashes, or tokens.
			w.log.Warn("refresh session cleanup batch failed", "error", err)
			return
		}
		total += n
		if n < int64(w.batchLimit) {
			break // fewer than a full batch => no more eligible rows
		}
	}
	if total > 0 {
		w.log.Info("refresh session cleanup completed", "deleted", total)
	}
}
