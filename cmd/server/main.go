package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"
	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/nanohype/portal/internal/domain"
	"github.com/nanohype/portal/internal/server"
	"github.com/nanohype/portal/internal/tracing"
)

// version is stamped into the trace resource (service.version); overridable via
// -ldflags at build time.
var version = "dev"

func main() {
	cfg := &domain.Config{}
	if err := env.Parse(cfg); err != nil {
		slog.Error("failed to parse config", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Distributed tracing (no-op when OTEL_TRACES_ENABLED is unset). A failure
	// to reach the collector must not stop the server from booting.
	tp, err := tracing.Init(context.Background(), "portal-server", version, cfg)
	if err != nil {
		logger.Warn("tracing init failed; continuing without traces", "error", err)
	}
	defer tracing.Shutdown(tp)

	// Connect to database with pool configuration
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to parse database URL", "error", err)
		os.Exit(1)
	}
	poolConfig.MaxConns = cfg.DBMaxConns
	poolConfig.MinConns = cfg.DBMinConns
	poolConfig.MaxConnIdleTime = cfg.DBMaxConnIdleTime
	poolConfig.HealthCheckPeriod = cfg.DBHealthCheckPeriod
	if cfg.TracingEnabled {
		poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()
	}

	dbPool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	if err := dbPool.Ping(context.Background()); err != nil {
		logger.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	logger.Info("connected to database")

	// Create server (sets up routes and handlers)
	srv := server.New(cfg, dbPool, logger)

	// Create River client (insert-only, no workers — workers run in the worker
	// process). The insert middleware stamps the current request's trace context
	// onto each enqueued job so the run continues the same trace.
	riverCfg := &river.Config{}
	if cfg.TracingEnabled {
		riverCfg.Middleware = []rivertype.Middleware{tracing.JobInsertMiddleware()}
	}
	riverClient, err := river.NewClient[pgx.Tx](riverpgxv5.New(dbPool), riverCfg)
	if err != nil {
		logger.Warn("failed to create river client, job enqueue disabled", "error", err)
	} else {
		srv.RunService().SetRiverClient(riverClient)
		srv.ApprovalService().SetRiverClient(riverClient)
		srv.PipelineService().SetRiverClient(riverClient)
		srv.ClusterService().SetRiverClient(riverClient)
		srv.TenantService().SetRiverClient(riverClient)
		srv.ClusterOrderService().SetRiverClient(riverClient)
		logger.Info("river client connected for job enqueue")
	}

	// Graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down server")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	logger.Info("server stopped")
}
