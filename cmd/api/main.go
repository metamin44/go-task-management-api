// Command api is the application composition root: configuration → logger →
// PostgreSQL pool → repositories → transactor → services → JWT manager →
// HTTP handlers → Echo server, wired with manual dependency injection.
//
// Migrations are NOT applied here: run cmd/migrate first (the Docker Compose
// stack does this as a one-shot service before the API starts). Startup fails
// fast and safely on invalid configuration or unreachable dependencies; all
// error messages are static or name-only — configuration values, DSNs, and
// secrets are never logged.
//
// Shutdown: SIGINT/SIGTERM trigger a graceful drain bounded by
// shutdownTimeout; the pool is closed afterwards. The slog JSON handler
// writes synchronously to stdout, so there are no buffered log resources to
// flush.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/metamin44/go-task-management-api/docs" // generated OpenAPI spec (swag init)
	"github.com/metamin44/go-task-management-api/internal/config"
	"github.com/metamin44/go-task-management-api/internal/database"
	"github.com/metamin44/go-task-management-api/internal/handler"
	"github.com/metamin44/go-task-management-api/internal/repository"
	"github.com/metamin44/go-task-management-api/internal/service"
)

const (
	// shutdownTimeout bounds the graceful drain after a shutdown signal.
	shutdownTimeout = 10 * time.Second
	// readinessTimeout bounds the /ready database ping.
	readinessTimeout = 3 * time.Second
)

func main() {
	if err := run(); err != nil {
		// All errors reaching here are secret-safe (config names reasons,
		// pool/runner/JWT errors are static).
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.NewPool(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Repositories (SQL-first, authorization-agnostic) + transactor.
	users := repository.NewUserRepository(pool)
	sessions := repository.NewRefreshSessionRepository(pool)
	resets := repository.NewPasswordResetTokenRepository(pool)
	projects := repository.NewProjectRepository(pool)
	members := repository.NewProjectMemberRepository(pool)
	tasks := repository.NewTaskRepository(pool)
	tx := repository.NewTransactor(pool)

	// Services (business rules, transactions, authorization).
	accessTokens, err := service.NewJWTAccessTokens([]byte(cfg.Auth.AccessSecret), cfg.Auth.AccessTTL)
	if err != nil {
		return err
	}
	hasher := service.NewArgon2idHasher()
	auth, err := service.NewAuthService(users, sessions, tx, hasher, accessTokens, cfg.Auth.RefreshTTL)
	if err != nil {
		return err
	}
	mailer := service.NewSMTPMailer(cfg.SMTP.Host, cfg.SMTP.Addr(), cfg.SMTP.Username, cfg.SMTP.Password, cfg.SMTP.From, cfg.SMTP.RequireTLS)
	resetSvc, err := service.NewPasswordResetService(users, resets, sessions, tx, hasher, mailer, cfg.Auth.ResetTTL, cfg.Auth.ResetBaseURL, logger)
	if err != nil {
		return err
	}
	userSvc := service.NewUserService(users)
	projectSvc := service.NewProjectService(projects, members, tx)
	memberSvc := service.NewMemberService(projects, members, users, tasks, tx)
	taskSvc := service.NewTaskService(tasks, members)

	// Background refresh-session cleanup (M-01). One goroutine drains
	// expired/revoked rows in bounded batches on a configurable tick; passes
	// never overlap within this instance. cleanupCtx is cancelled before we
	// join the worker, so shutdown never deadlocks — including the early
	// server-error return path where the signal context is not yet cancelled.
	cleanupCtx, cleanupCancel := context.WithCancel(ctx)
	cleanupWorker := service.NewCleanupWorker(sessions, cfg.Auth.CleanupInterval, logger)
	var cleanupWG sync.WaitGroup
	cleanupWG.Add(1)
	go func() {
		defer cleanupWG.Done()
		cleanupWorker.Run(cleanupCtx)
	}()
	// LIFO defers: cleanupCancel runs first (stops the worker), then Wait
	// (joins it), then pool.Close and signal stop (registered earlier).
	defer cleanupWG.Wait()
	defer cleanupCancel()

	// HTTP layer. CORS stays disabled (no AllowedOrigins): same-origin only
	// until an explicit whitelist is approved for a deployment.
	api := handler.New(auth, resetSvc, userSvc, projectSvc, memberSvc, taskSvc)
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	api.RegisterRoutes(e, handler.Options{
		Logger:                logger,
		AuthRateLimitInterval: cfg.Auth.RateLimitInterval,
		ReadinessCheck: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, readinessTimeout)
			defer cancel()
			return pool.Ping(pingCtx)
		},
	})
	e.GET("/swagger/*", echoSwagger.WrapHandler)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.App.HTTPPort),
		Handler:      e,
		ReadTimeout:  cfg.App.HTTPReadTimeout,
		WriteTimeout: cfg.App.HTTPWriteTimeout,
		IdleTimeout:  cfg.App.HTTPIdleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("api starting", "env", cfg.App.Env, "port", cfg.App.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Static message: the raw listener error is not exposed.
			serverErr <- fmt.Errorf("http server failed to start or crashed")
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown timed out; forcing close")
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown failed")
	}
	logger.Info("api stopped")
	return nil
}
