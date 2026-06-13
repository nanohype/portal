package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"path/filepath"
	"sync"

	tofuaws "github.com/nanohype/portal/internal/aws"
	"github.com/nanohype/portal/internal/domain"
	tofugit "github.com/nanohype/portal/internal/git"
	tofuhelm "github.com/nanohype/portal/internal/helm"
	"github.com/nanohype/portal/internal/k8s"
	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/worker"
	"github.com/nanohype/portal/internal/worker/executor"
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
		cfg := k8s.SlimConfig{
			APIEndpoint: creds.APIEndpoint,
			CABundle:    creds.CABundle,
		}
		// eks_iam clusters store no token — mint a short-lived EKS token per
		// request by assuming the parent account's role and presigning STS. The
		// built k8s client is cached while the token underneath rotates, so
		// nothing long-lived is held. sa_token clusters use the stored token.
		if c.AuthMode == service.AuthModeEKSIAM {
			if awsProvider == nil {
				return k8s.SlimConfig{}, fmt.Errorf("eks_iam cluster %q requires an AWS provider, but none is configured", c.Name)
			}
			account, err := queries.GetAccount(context.Background(), repository.GetAccountParams{ID: c.AccountID, OrgID: c.OrgID})
			if err != nil {
				return k8s.SlimConfig{}, fmt.Errorf("load account for eks_iam cluster %q: %w", c.Name, err)
			}
			if account.AssumeRoleARN == "" {
				return k8s.SlimConfig{}, fmt.Errorf("eks_iam cluster %q account has no assume-role ARN", c.Name)
			}
			cfg.TokenSource = awsProvider.EKSTokenSource(account.AssumeRoleARN, "", c.Region, c.EKSClusterName)
			return cfg, nil
		}
		cfg.BearerToken = creds.SAToken
		return cfg, nil
	}
	clusterStatusUpdate := func(ctx context.Context, id, orgID, status, errMsg, k8sVersion string, nodeCount int32) error {
		return clusterSvc.SetConnectionStatus(ctx, id, orgID, status, errMsg, k8sVersion, nodeCount)
	}
	clusterTestWorker := worker.NewClusterConnectionTestJobWorker(queries, clusterDecrypt, clusterStatusUpdate, awsProvider, k8sCache)
	river.AddWorker(workers, clusterTestWorker)

	// Cluster watcher: walks each connected cluster's eks-agent-platform CRDs periodically
	// and reconciles portal's tenant inventory. Worker = process one cluster;
	// dispatch tick (further below) fans out one job per cluster every 60s.
	tenantSvc := service.NewTenantService(queries, dbPool)
	tenantReconcile := func(ctx context.Context, orgID, clusterID string, observed []worker.TenantSnapshot) (int, int, error) {
		svcObs := make([]service.TenantSnapshot, len(observed))
		for i, o := range observed {
			svcObs[i] = service.TenantSnapshot{Name: o.Name, Phase: o.Phase, Spec: o.Spec, Status: o.Status}
		}
		return tenantSvc.Reconcile(ctx, orgID, clusterID, svcObs)
	}
	clusterWatchWorker := worker.NewClusterWatchJobWorker(queries, clusterDecrypt, tenantReconcile)
	river.AddWorker(workers, clusterWatchWorker)

	clusterOrderSvc := service.NewClusterOrderService(queries, dbPool)

	// Tenant write path (phase 2c): renders the eks-agent-platform `charts/tenant` chart
	// with the user-supplied values, commits the rendered manifest into the
	// tenants repo, lets ArgoCD reconcile. Two git repos are involved:
	//  * eks-agent-platform charts repo — read-only mirror, cloned at startup, pulled on
	//    each tenant op so chart edits land without a worker redeploy.
	//  * tenants repo — read-write, where rendered manifests get committed.
	// Both are optional: if URLs aren't set, the apply worker surfaces a
	// clear "not configured" error rather than crashing at boot.
	var tenantApplyWorker *worker.TenantApplyJobWorker
	if cfg.TenantsRepoURL != "" && cfg.EksAgentPlatformChartsRepoURL != "" && cfg.GitSSHKeyPath != "" {
		eksAgentPlatformRepo, err := tofugit.NewRepo(filepath.Join(cfg.GitCacheDir, "eks-agent-platform"), cfg.EksAgentPlatformChartsRepoURL, cfg.GitSSHKeyPath)
		if err != nil {
			logger.Error("failed to initialize eks-agent-platform charts repo", "error", err)
			os.Exit(1)
		}
		if err := eksAgentPlatformRepo.CloneOrPull(context.Background(), cfg.EksAgentPlatformChartsRepoRef); err != nil {
			logger.Warn("eks-agent-platform charts initial sync failed (will retry on first tenant op)", "error", err)
		}
		tenantsRepo, err := tofugit.NewRepo(filepath.Join(cfg.GitCacheDir, "tenants"), cfg.TenantsRepoURL, cfg.GitSSHKeyPath)
		if err != nil {
			logger.Error("failed to initialize tenants repo", "error", err)
			os.Exit(1)
		}

		chartCache := tofuhelm.NewCache(eksAgentPlatformRepo.Workdir())
		renderFn := func(chartName, releaseName, namespace string, values map[string]interface{}) (string, error) {
			// Pull fresh chart on every render so chart-author edits land
			// without a portal restart. Cheap (~few hundred ms when nothing
			// changed); the chartCache.Reset call discards in-memory parses
			// so the next Load re-reads.
			if err := eksAgentPlatformRepo.CloneOrPull(context.Background(), cfg.EksAgentPlatformChartsRepoRef); err != nil {
				return "", err
			}
			chartCache.Reset()
			ch, err := chartCache.Load(chartName)
			if err != nil {
				return "", err
			}
			return tofuhelm.Render(ch, releaseName, namespace, values)
		}

		tenantApplyWorker = worker.NewTenantApplyJobWorker(worker.TenantApplyDeps{
			Queries: queries,
			LoadOp: func(ctx context.Context, id, orgID string) (repository.TenantOperation, error) {
				return tenantSvc.GetOperation(ctx, id, orgID)
			},
			CompleteOp: func(ctx context.Context, id, orgID, status, sha, errMsg string) error {
				return tenantSvc.CompleteOperation(ctx, id, orgID, status, sha, errMsg)
			},
			Render:      renderFn,
			TenantsRepo: tenantsRepo,
			RepoMu:      &sync.Mutex{},
			TenantsRef:  cfg.TenantsRepoRef,
			Author:      tofugit.Author{Name: cfg.GitAuthorName, Email: cfg.GitAuthorEmail},
		})
		river.AddWorker(workers, tenantApplyWorker)
		logger.Info("tenant write path enabled",
			"eks_agent_platform_charts", cfg.EksAgentPlatformChartsRepoURL,
			"tenants_repo", cfg.TenantsRepoURL,
		)
	} else {
		// Register a stub that fails clearly when invoked — without this,
		// River would reject jobs of an unknown kind with a non-actionable
		// "no worker for kind" error and the tenant_operations row would
		// be stuck in pending. Better to surface "not configured" on the
		// row itself so the UI shows what's wrong.
		stub := worker.NewTenantApplyJobWorker(worker.TenantApplyDeps{
			Queries: queries,
			LoadOp: func(ctx context.Context, id, orgID string) (repository.TenantOperation, error) {
				return tenantSvc.GetOperation(ctx, id, orgID)
			},
			CompleteOp: func(ctx context.Context, id, orgID, status, sha, errMsg string) error {
				return tenantSvc.CompleteOperation(ctx, id, orgID, status, sha, errMsg)
			},
			Render:     nil,
			RepoMu:     &sync.Mutex{},
			TenantsRef: cfg.TenantsRepoRef,
		})
		river.AddWorker(workers, stub)
		logger.Info("tenant write path disabled (GITOPS_TENANTS_REPO_URL / EKS_AGENT_PLATFORM_CHARTS_REPO_URL / GITOPS_SSH_KEY_PATH not set)")
	}

	// Cluster vend path: templates the eks-fleet Cluster CR (no chart — the CR is
	// small + flat) and commits it to the clusters repo for the hub's ArgoCD to
	// reconcile. Optional: if GITOPS_CLUSTERS_REPO_URL / GITOPS_SSH_KEY_PATH aren't
	// set, the worker surfaces a clear "not configured" error rather than crashing.
	var clusterApplyWorker *worker.ClusterApplyJobWorker
	if cfg.ClustersRepoURL != "" && cfg.GitSSHKeyPath != "" {
		clustersRepo, err := tofugit.NewRepo(filepath.Join(cfg.GitCacheDir, "clusters"), cfg.ClustersRepoURL, cfg.GitSSHKeyPath)
		if err != nil {
			logger.Error("failed to initialize clusters repo", "error", err)
			os.Exit(1)
		}
		clusterApplyWorker = worker.NewClusterApplyJobWorker(worker.ClusterApplyDeps{
			LoadOp: func(ctx context.Context, id, orgID string) (repository.ClusterOperation, error) {
				return clusterOrderSvc.GetOperation(ctx, id, orgID)
			},
			CompleteOp: func(ctx context.Context, id, orgID, status, sha, errMsg string) error {
				return clusterOrderSvc.CompleteOperation(ctx, id, orgID, status, sha, errMsg)
			},
			ClustersRepo: clustersRepo,
			RepoMu:       &sync.Mutex{},
			ClustersRef:  cfg.ClustersRepoRef,
			Author:       tofugit.Author{Name: cfg.GitAuthorName, Email: cfg.GitAuthorEmail},
			HubRoleArn:   cfg.FleetHubRoleArn,
		})
		river.AddWorker(workers, clusterApplyWorker)
		logger.Info("cluster vend path enabled", "clusters_repo", cfg.ClustersRepoURL)
	} else {
		stub := worker.NewClusterApplyJobWorker(worker.ClusterApplyDeps{
			LoadOp: func(ctx context.Context, id, orgID string) (repository.ClusterOperation, error) {
				return clusterOrderSvc.GetOperation(ctx, id, orgID)
			},
			CompleteOp: func(ctx context.Context, id, orgID, status, sha, errMsg string) error {
				return clusterOrderSvc.CompleteOperation(ctx, id, orgID, status, sha, errMsg)
			},
			RepoMu:      &sync.Mutex{},
			ClustersRef: cfg.ClustersRepoRef,
		})
		river.AddWorker(workers, stub)
		logger.Info("cluster vend path disabled (GITOPS_CLUSTERS_REPO_URL / GITOPS_SSH_KEY_PATH not set)")
	}

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
	clusterTestWorker.SetRiverClient(riverClient, dbPool)
	clusterWatchWorker.SetRiverClient(riverClient, dbPool)
	if tenantApplyWorker != nil {
		tenantApplyWorker.SetRiverClient(riverClient, dbPool)
	}
	if clusterApplyWorker != nil {
		clusterApplyWorker.SetRiverClient(riverClient, dbPool)
	}
	runSvc.SetRiverClient(riverClient)
	clusterSvc.SetRiverClient(riverClient) // so the ArgoCD sync can enqueue connection-tests
	clusterOrderSvc.SetRiverClient(riverClient)

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

	// Cluster-watch dispatcher: every 60s, find all connected clusters and
	// enqueue a watch job per cluster. River's UniqueOpts on the job args
	// drops duplicates if a previous tick's job is still running, so a slow
	// cluster doesn't backlog the queue. Shuts down with the signal context.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		runWatchDispatch := func() {
			targets, err := queries.ListConnectedClusters(context.Background())
			if err != nil {
				logger.Warn("watch dispatch: list connected clusters", "error", err)
				return
			}
			for _, target := range targets {
				_, err := riverClient.Insert(context.Background(), worker.ClusterWatchJobArgs{
					ClusterID: target.ID, OrgID: target.OrgID,
				}, nil)
				if err != nil {
					logger.Warn("watch dispatch: insert job", "cluster_id", target.ID, "error", err)
				}
			}
		}
		// First tick: don't wait 60s for the first sweep after a restart.
		runWatchDispatch()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runWatchDispatch()
			}
		}
	}()

	// ArgoCD cluster-registry sync: when enabled and running in-cluster, read
	// ArgoCD's cluster Secrets and upsert the inventory, so a cluster registered
	// with ArgoCD is onboarded without a manual portal registration. Inert
	// unless ARGOCD_CLUSTER_SYNC and the org/account/created-by IDs are set.
	if cfg.ArgoCDClusterSync && cfg.ArgoCDSyncOrgID != "" && cfg.ArgoCDSyncAccountID != "" && cfg.ArgoCDSyncCreatedBy != "" {
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			logger.Warn("argocd sync enabled but worker is not running in-cluster; skipping", "error", err)
		} else if argocdClient, err := kubernetes.NewForConfig(restCfg); err != nil {
			logger.Warn("argocd sync: failed to build in-cluster client; skipping", "error", err)
		} else {
			syncSvc := service.NewArgoCDSyncService(clusterSvc, argocdClient, cfg.ArgoCDNamespace,
				cfg.ArgoCDSyncOrgID, cfg.ArgoCDSyncAccountID, cfg.ArgoCDSyncCreatedBy)
			go func() {
				t := time.NewTicker(cfg.ArgoCDSyncInterval)
				defer t.Stop()
				runSync := func() {
					created, updated, skipped, err := syncSvc.Sync(context.Background())
					if err != nil {
						logger.Warn("argocd sync", "error", err)
						return
					}
					logger.Info("argocd sync tick", "created", created, "updated", updated, "skipped", skipped)
				}
				runSync()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						runSync()
					}
				}
			}()
		}
	}

	// Cluster provision watch-back: the vend loop's closing leg. When enabled
	// and running in-cluster on the hub, poll each committed provision op's
	// eks-fleet Cluster XR and, once its EKS endpoint + CA are up, auto-register
	// the new cluster (as eks_iam — no stored token) and flip the op to 'active'.
	// Inert in dev / off the hub, where there's no in-cluster config.
	if cfg.ClusterWatchback {
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			logger.Warn("cluster watch-back enabled but worker is not running in-cluster; skipping", "error", err)
		} else if hubDyn, err := dynamic.NewForConfig(restCfg); err != nil {
			logger.Warn("cluster watch-back: failed to build in-cluster dynamic client; skipping", "error", err)
		} else {
			watchSvc := service.NewClusterProvisionWatchService(clusterSvc, queries, hubDyn)
			go func() {
				t := time.NewTicker(cfg.ClusterWatchbackInterval)
				defer t.Stop()
				runWatchback := func() {
					completed, pending, err := watchSvc.Sync(context.Background())
					if err != nil {
						logger.Warn("cluster watch-back", "error", err)
						return
					}
					if completed > 0 || pending > 0 {
						logger.Info("cluster watch-back tick", "completed", completed, "pending", pending)
					}
				}
				runWatchback()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						runWatchback()
					}
				}
			}()
		}
	}

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
