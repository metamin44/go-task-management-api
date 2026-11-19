package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// spyCleaner is a configurable SessionCleaner double recording call counts and
// peak concurrency, so tests can assert bounded, non-overlapping execution.
// The worker calls it from a single goroutine, so the plain fn field is safe.
type spyCleaner struct {
	fn            func(ctx context.Context, retention time.Duration, limit int) (int64, error)
	calls         atomic.Int64
	concurrent    atomic.Int64
	maxConcurrent atomic.Int64
}

func (s *spyCleaner) DeleteExpiredBatch(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	s.calls.Add(1)
	cur := s.concurrent.Add(1)
	for {
		old := s.maxConcurrent.Load()
		if cur <= old || s.maxConcurrent.CompareAndSwap(old, cur) {
			break
		}
	}
	defer s.concurrent.Add(-1)
	return s.fn(ctx, retention, limit)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// The worker drains batches until one returns fewer rows than the limit, and
// preserves the finalized 30-day retention / 500-row batch policy.
func TestCleanupWorkerDrainsUntilExhausted(t *testing.T) {
	var gotRetention time.Duration
	var gotLimit int
	seq := []int64{cleanupBatchLimit, cleanupBatchLimit, 3}
	var i int
	c := &spyCleaner{fn: func(_ context.Context, r time.Duration, l int) (int64, error) {
		gotRetention, gotLimit = r, l
		n := seq[i]
		i++
		return n, nil
	}}
	w := NewCleanupWorker(c, time.Hour, discardLogger())
	w.runOnce(context.Background())
	if got := c.calls.Load(); got != 3 {
		t.Fatalf("expected 3 batches (drain until partial), got %d", got)
	}
	if gotRetention != cleanupRetention || gotLimit != cleanupBatchLimit {
		t.Errorf("policy not preserved: retention=%v limit=%d", gotRetention, gotLimit)
	}
}

// A never-draining backlog must not loop unboundedly within one tick.
func TestCleanupWorkerBoundedPerTick(t *testing.T) {
	c := &spyCleaner{fn: func(context.Context, time.Duration, int) (int64, error) {
		return cleanupBatchLimit, nil // always a full batch
	}}
	w := NewCleanupWorker(c, time.Hour, discardLogger())
	w.runOnce(context.Background())
	if got := c.calls.Load(); got != cleanupMaxBatches {
		t.Fatalf("per-tick work must be bounded to %d batches, got %d", cleanupMaxBatches, got)
	}
}

// An error stops the pass after one batch and is logged with a secret-safe message.
func TestCleanupWorkerErrorLogsAndStops(t *testing.T) {
	var buf bytes.Buffer
	c := &spyCleaner{fn: func(context.Context, time.Duration, int) (int64, error) {
		return 0, errors.New("injected cleanup failure")
	}}
	w := NewCleanupWorker(c, time.Hour, slog.New(slog.NewTextHandler(&buf, nil)))
	w.runOnce(context.Background())
	if got := c.calls.Load(); got != 1 {
		t.Fatalf("must stop after first error, got %d calls", got)
	}
	if !strings.Contains(buf.String(), "cleanup batch failed") {
		t.Errorf("error must be logged safely, got %q", buf.String())
	}
}

// Run returns promptly on an already-cancelled context and executes nothing.
func TestCleanupWorkerRunCancellation(t *testing.T) {
	c := &spyCleaner{fn: func(context.Context, time.Duration, int) (int64, error) { return 0, nil }}
	w := NewCleanupWorker(c, time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return promptly on cancelled context")
	}
	if got := c.calls.Load(); got != 0 {
		t.Errorf("cancelled context must not execute cleanup, got %d calls", got)
	}
}

// Cancellation mid-pass bounds database work: the loop checks ctx before each
// batch, so no further batch runs after cancellation.
func TestCleanupWorkerCancellationMidPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var once sync.Once
	c := &spyCleaner{fn: func(context.Context, time.Duration, int) (int64, error) {
		once.Do(func() { <-release }) // block the first batch only
		return cleanupBatchLimit, nil // full batch => loop would continue
	}}
	w := NewCleanupWorker(c, time.Hour, discardLogger())
	done := make(chan struct{})
	go func() { w.runOnce(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for c.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.calls.Load() == 0 {
		cancel()
		close(release)
		t.Fatal("first batch never started")
	}
	cancel()       // cancel while the first batch is still blocked
	close(release) // let it finish; the next iteration must see ctx.Err()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOnce must return after mid-pass cancellation")
	}
	if got := c.calls.Load(); got != 1 {
		t.Errorf("no batch may run after cancellation, got %d calls", got)
	}
}

// Passes never overlap: while one pass is blocked, subsequent ticks must not
// start a concurrent pass (single-goroutine sequential execution).
func TestCleanupWorkerNoOverlap(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	c := &spyCleaner{fn: func(context.Context, time.Duration, int) (int64, error) {
		once.Do(func() { <-release }) // first pass blocks; later passes return at once
		return 0, nil
	}}
	w := NewCleanupWorker(c, time.Millisecond, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for c.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.calls.Load() == 0 {
		cancel()
		close(release)
		t.Fatal("first cleanup pass never started")
	}
	// Let several ticks elapse while the first pass is still blocked.
	time.Sleep(10 * time.Millisecond)
	if got := c.maxConcurrent.Load(); got != 1 {
		t.Errorf("passes must never overlap, peak concurrency = %d", got)
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return after cancellation")
	}
	if got := c.maxConcurrent.Load(); got != 1 {
		t.Errorf("peak concurrency = %d, want 1", got)
	}
}

// Non-positive interval falls back to the default; policy constants are applied;
// a nil logger is replaced with a non-nil default.
func TestCleanupWorkerDefaults(t *testing.T) {
	c := &spyCleaner{fn: func(context.Context, time.Duration, int) (int64, error) { return 0, nil }}
	w := NewCleanupWorker(c, 0, nil)
	if w.interval != DefaultCleanupInterval {
		t.Errorf("non-positive interval must default to %v, got %v", DefaultCleanupInterval, w.interval)
	}
	if w.retention != cleanupRetention || w.batchLimit != cleanupBatchLimit || w.maxBatches != cleanupMaxBatches {
		t.Errorf("policy constants not applied: retention=%v limit=%d max=%d", w.retention, w.batchLimit, w.maxBatches)
	}
	if w.log == nil {
		t.Error("nil logger must fall back to a non-nil default")
	}
}
