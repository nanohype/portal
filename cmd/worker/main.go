package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	tofuaws "github.com/stxkxs/tofui/internal/aws"
	"github.com/stxkxs/tofui/internal/domain"
	"github.com/stxkxs/tofui/internal/k8s"
	"github.com/stxkxs/tofui/internal/logstream"
	"github.com/stxkxs/tofui/internal/repository"
	"github.com/stxkxs/tofui/internal/secrets"
	"github.com/stxkxs/tofui/internal/service"
	"github.com/stxkxs/tofui/internal/storage"
	"github.com/stxkxs/tofui/internal/worker"
	"github.com/stxkxs/tofui/internal/worker/executor"
)

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

	queries := repository.New(dbPool)

	var streamer logstream.Streamer
	if cfg.RedisURL != "" {
		rs, err := logstream.NewRedisStreamer(cfg.RedisURL)
		if err != nil {
			logger.Warn("redis streamer not available, falling back to memory", "error", err)
			streamer = logstream.NewMemoryStreamer()
		} else {
			streamer = rs
			logger.Info("using redis log streamer")
		}
	} else {
		streamer = logstream.NewMemoryStreamer()
	}

	// Optional S3 storage
	var store *storage.S3Storage
	if cfg.S3Endpoint != "" {
		s, err := storage.NewS3Storage(cfg)
		if err != nil {
			logger.Warn("S3 storage not available, logs and state won't be persisted", "error", err)
		} else {
			if err := s.EnsureBucket(context.Background()); err != nil {
				logger.Warn("failed to ensure S3 bucket", "error", err)
			} else {
				store = s
				logger.Info("S3 storage connected", "bucket", cfg.S3Bucket)
			}
		}
	}

	// Optional encryptor for decrypting sensitive variables
	var encryptor *secrets.Encryptor
	if cfg.EncryptionKey != "" {
		enc, err := secrets.NewEncryptor(cfg.EncryptionKey)
		if err != nil {
			logger.Warn("encryption not available, sensitive values will be passed as-is", "error", err)
		} else {
			encryptor = enc
		}
	}

	// Create executor
	var exec executor.Executor
	switch cfg.ExecutorType {
	case "kubernetes":
		k8sExec, err := executor.NewKubernetesExecutor(executor.KubernetesExecutorConfig{
			Namespace:   cfg.ExecutorNamespace,
			Image:       cfg.ExecutorImage,
			ImagePrefix: cfg.ExecutorImagePrefix,
		})
		if err != nil {
			logger.Error("failed to create kubernetes executor", "error", err)
			os.Exit(1)
		}
		exec = k8sExec
		logger.Info("using kubernetes executor", "namespace", cfg.ExecutorNamespace, "image", cfg.ExecutorImage)
	default:
		exec = executor.NewLocalExecutor()
		logger.Info("using local executor")
	}

	// Set up RunService for pipeline stage worker
	runSvc := service.NewRunService(queries, dbPool, streamer)

	// Set up River workers
	workers := river.NewWorkers()
	runJobWorker := worker.NewRunJobWorker(queries, exec, streamer, store, encryptor)
	river.AddWorker(workers, runJobWorker)

	// Pipeline stage worker with function adapters to avoid import cycle
	createRunFn := func(ctx context.Context, workspaceID, orgID, operation, createdBy string, autoApplyOverride *bool) (repository.Run, error) {
		return runSvc.Create(ctx, service.CreateRunParams{
			WorkspaceID:       workspaceID,
			OrgID:             orgID,
			Operation:         operation,
			CreatedBy:         createdBy,
			AutoApplyOverride: autoApplyOverride,
		})
	}
	pipelineStageWorker := worker.NewPipelineStageJobWorker(queries, createRunFn, service.ImportOutputsBetweenWorkspaces, store)
	river.AddWorker(workers, pipelineStageWorker)

	// Cluster connection-test worker (proves stored cluster credentials work).
	// AWS provider is best-effort — if the default credential chain can't load
	// (no profile, no IRSA, no env), the worker still runs the k8s probe and
	// just skips the sts:GetCallerIdentity verification step.
	clusterSvc := service.NewClusterService(queries, dbPool, encryptor)
	awsProvider, err := tofuaws.NewProvider(context.Background())
	if err != nil {
		logger.Warn("aws provider not available, sts verification disabled", "error", err)
		awsProvider = nil
	}
	k8sCache := k8s.NewClientCache()
	clusterDecrypt := func(c repository.Cluster) (k8s.SlimConfig, error) {
		creds, err := clusterSvc.Decrypt(c)
		if err != nil {
			return k8s.SlimConfig{}, err
		}
		return k8s.SlimConfig{
			APIEndpoint: creds.APIEndpoint,
			CABundle:    creds.CABundle,
			BearerToken: creds.SAToken,
		}, nil
	}
	clusterStatusUpdate := func(ctx context.Context, id, orgID, status, errMsg, k8sVersion string, nodeCount int32) error {
		return clusterSvc.SetConnectionStatus(ctx, id, orgID, status, errMsg, k8sVersion, nodeCount)
	}
	clusterTestWorker := worker.NewClusterConnectionTestJobWorker(queries, clusterDecrypt, clusterStatusUpdate, awsProvider, k8sCache)
	river.AddWorker(workers, clusterTestWorker)

	// Create River client
	riverClient, err := river.NewClient[pgx.Tx](riverpgxv5.New(dbPool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: cfg.WorkerConcurrency},
		},
		Workers: workers,
	})
	if err != nil {
		logger.Error("failed to create river client", "error", err)
		os.Exit(1)
	}

	// Wire river client back to workers for enqueueing
	runJobWorker.SetRiverClient(riverClient, dbPool)
	pipelineStageWorker.SetRiverClient(riverClient, dbPool)
	runSvc.SetRiverClient(riverClient)

	// Health endpoint for K8s liveness probe
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		if err := http.ListenAndServe(cfg.WorkerHealthAddr, mux); err != nil {
			logger.Error("health server failed", "error", err)
		}
	}()

	// Start River client with a separate context so in-flight jobs aren't killed on signal
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := riverClient.Start(context.Background()); err != nil {
		logger.Error("failed to start river client", "error", err)
		os.Exit(1)
	}

	logger.Info("worker started", "concurrency", cfg.WorkerConcurrency)

	<-ctx.Done()
	logger.Info("shutting down worker, draining in-flight jobs...")

	// Give in-flight jobs time to complete before force-stopping
	stopCtx, stopCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer stopCancel()

	if err := riverClient.Stop(stopCtx); err != nil {
		logger.Error("river client stop error (some jobs may not have finished)", "error", err)
	} else {
		logger.Info("worker stopped gracefully")
	}
}
