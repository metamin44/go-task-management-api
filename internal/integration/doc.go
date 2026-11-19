// Package integration holds the live-PostgreSQL integration suite (T1–T4
// from PROJECT_CONTEXT.md §8). Every test file carries the "integration"
// build tag, so ordinary unit tests never require Docker:
//
//	go test -count=1 ./...                                # unit only (no Docker)
//	go test -count=1 -tags=integration ./internal/integration/...   # T1–T4
//
// The suite starts its own isolated, temporary postgres:17-alpine container
// via Testcontainers, applies the project's embedded migrations through the
// real migration runner, and terminates the container afterwards (Ryuk also
// reaps it on abnormal exits). It never connects to the development database
// (task_management / task-management-db) and never reads .env; credentials
// are test-only dummy values defined in the harness.
package integration
