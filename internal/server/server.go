package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/config"
	"github.com/nanohype/portal/internal/handler"
	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/metrics"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/tracing"
)

// Option configures a Server before its router is built.
type Option func(*Server)

// WithAuthzResolver replaces the source the gates read authorization from — the
// caller's current org role and their teams' grants on a workspace. Production
// passes service.AuthzService, which reads both from Postgres. Tests pass a
// resolver they control, so the authorization behaviour of the real router can
// be asserted without a database standing in the way.
func WithAuthzResolver(resolver AuthzResolver) Option {
	return func(s *Server) { s.authz = resolver }
}

// AuthzResolver is everything the gates need to decide a request: who the
// caller is right now, and what their teams hold on one workspace.
type AuthzResolver interface {
	auth.UserRoleResolver
	auth.WorkspaceRoleResolver
}

type Server struct {
	authz           AuthzResolver
	cfg             *config.Config
	router          chi.Router
	db              *pgxpool.Pool
	logger          *slog.Logger
	http            *http.Server
	approvalHandler *handler.ApprovalHandler
	approvalSvc     *service.ApprovalService
	runSvc          *service.RunService
	pipelineSvc     *service.PipelineService
	clusterSvc      *service.ClusterService
	tenantSvc       *service.TenantService
	clusterOrderSvc *service.ClusterOrderService
}

func New(cfg *config.Config, db *pgxpool.Pool, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		cfg:    cfg,
		db:     db,
		logger: logger,
	}
	for _, opt := range opts {
		opt(s)
	}

	s.setupRouter()
	s.http = &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

func (s *Server) RunService() *service.RunService {
	return s.runSvc
}

func (s *Server) PipelineService() *service.PipelineService {
	return s.pipelineSvc
}

func (s *Server) ClusterService() *service.ClusterService {
	return s.clusterSvc
}

func (s *Server) TenantService() *service.TenantService {
	return s.tenantSvc
}

func (s *Server) ClusterOrderService() *service.ClusterOrderService {
	return s.clusterOrderSvc
}

func (s *Server) ApprovalService() *service.ApprovalService {
	return s.approvalSvc
}

func (s *Server) setupRouter() {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(tracing.HTTPMiddleware) // server span, joined to incoming W3C context
	r.Use(metrics.Middleware)     // HTTP RED, keyed on the matched route pattern
	r.Use(NewStructuredLogger(s.logger))
	r.Use(middleware.Recoverer)
	r.Use(NewRateLimiter(100, 200).Middleware) // 100 req/s per IP, burst 200
	r.Use(SecurityHeaders)
	r.Use(middleware.Compress(5))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: func() []string {
			origins := []string{s.cfg.WebURL}
			if s.cfg.Environment == "development" {
				origins = append(origins, "http://localhost:5173")
			}
			return origins
		}(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Prometheus metrics. /metrics is unauthenticated on purpose — the in-cluster
	// Grafana Agent scrapes the pod directly; the ingress should not route it.
	reg := metrics.Register()
	metrics.RegisterPool(reg, s.db)
	r.Handle("/metrics", metrics.Handler(reg))

	queries := repository.New(s.db)

	var streamer logstream.Streamer
	if s.cfg.RedisURL != "" {
		rs, err := logstream.NewRedisStreamer(s.cfg.RedisURL)
		if err != nil {
			s.logger.Warn("redis streamer not available, falling back to memory", "error", err)
			streamer = logstream.NewMemoryStreamer()
		} else {
			streamer = rs
			s.logger.Info("using redis log streamer")
		}
	} else {
		streamer = logstream.NewMemoryStreamer()
	}
	jwtAuth := auth.NewJWTAuth(s.cfg.JWTSecret, s.cfg.JWTExpiration)

	// One authorization source for the whole router: the authentication
	// middleware reads the caller's org role from it on every request, and every
	// workspace gate reads their team grants from it. Sharing one resolver is
	// what keeps a single request from being decided against two different
	// pictures of who the caller is.
	if s.authz == nil {
		s.authz = service.NewAuthzService(queries)
	}
	authMiddleware := auth.NewMiddleware(jwtAuth, s.authz)

	// Optional S3 storage
	var store *storage.S3Storage
	if s.cfg.S3Endpoint != "" {
		var err error
		store, err = storage.NewS3Storage(s.cfg)
		if err != nil {
			s.logger.Warn("S3 storage not available", "error", err)
		}
	}

	// Optional encryptor
	var encryptor *secrets.Encryptor
	if s.cfg.EncryptionKey != "" {
		var err error
		encryptor, err = secrets.NewEncryptor(s.cfg.EncryptionKey)
		if err != nil {
			s.logger.Warn("encryption not available", "error", err)
		}
	}

	auditSvc := service.NewAuditService(queries)
	s.runSvc = service.NewRunService(queries, s.db, streamer)

	userSvc := service.NewUserService(queries)
	authHandler := handler.NewAuthHandler(s.cfg, userSvc, jwtAuth)
	workspaceSvc := service.NewWorkspaceService(queries, s.db, store)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceSvc, auditSvc, store, queries)
	accountSvc := service.NewAccountService(queries, s.db, encryptor)
	accountHandler := handler.NewAccountHandler(accountSvc, auditSvc)
	s.clusterSvc = service.NewClusterService(queries, s.db, encryptor)
	clusterHandler := handler.NewClusterHandler(s.clusterSvc, accountSvc, auditSvc)
	s.tenantSvc = service.NewTenantService(queries, s.db)
	templateSvc := service.NewTemplateService(queries, s.db)
	accessSvc := service.NewTeamAccessService(queries, s.db)
	templateHandler := handler.NewTemplateHandler(templateSvc, accessSvc, auditSvc)
	tenantHandler := handler.NewTenantHandler(s.tenantSvc, templateSvc, accessSvc, auditSvc)
	s.clusterOrderSvc = service.NewClusterOrderService(queries, s.db)
	clusterOrderHandler := handler.NewClusterOrderHandler(s.clusterOrderSvc, auditSvc)
	opsHandler := handler.NewOpsHandler(service.NewOpsFeedService(queries))
	wsOrigins := []string{s.cfg.WebURL}
	if s.cfg.Environment == "development" {
		wsOrigins = append(wsOrigins, "http://localhost:5173")
	}
	runHandler := handler.NewRunHandler(s.runSvc, workspaceSvc, streamer, auditSvc, wsOrigins, store)
	discoverySvc := service.NewDiscoveryService(queries, store)
	variableHandler := handler.NewVariableHandler(queries, encryptor, auditSvc, workspaceSvc, discoverySvc, s.authz)
	teamSvc := service.NewTeamService(queries)
	teamHandler := handler.NewTeamHandler(teamSvc, auditSvc)
	stateSvc := service.NewStateService(queries, store)
	stateHandler := handler.NewStateHandler(stateSvc, auditSvc)
	s.approvalSvc = service.NewApprovalService(queries, s.db, auditSvc)
	s.approvalHandler = handler.NewApprovalHandler(s.approvalSvc)
	auditHandler := handler.NewAuditHandler(queries)
	healthHandler := handler.NewHealthHandler(s.db, s.cfg.Environment)
	userHandler := handler.NewUserHandler(userSvc, auditSvc)
	orgVarHandler := handler.NewOrgVariableHandler(queries, encryptor, auditSvc)
	pipelineVarHandler := handler.NewPipelineVariableHandler(queries, encryptor, auditSvc)
	s.pipelineSvc = service.NewPipelineService(queries, s.db, s.runSvc)
	pipelineHandler := handler.NewPipelineHandler(s.pipelineSvc, auditSvc)
	webhookHandler := handler.NewWebhookHandler(queries, s.runSvc, auditSvc, s.cfg.WebhookSecret)

	// Probes. /healthz is process-only liveness; /readyz gates traffic on
	// Postgres. /api/v1/health (below) is the app-level health surface the UI
	// reads.
	r.Get("/healthz", healthHandler.Live)
	r.Get("/readyz", healthHandler.Ready)

	// Workspace-scoped gates. These resolve the caller's org role together with
	// any workspace_team_access grant their teams hold on the workspace in the
	// URL, so an admin can hand one team elevated rights on one workspace
	// without moving anybody's org role. Grants only ever elevate — see
	// auth.RequireWorkspaceRole.
	wsView := auth.RequireWorkspaceAction(s.authz, auth.ActionViewWorkspace)
	wsRun := auth.RequireWorkspaceAction(s.authz, auth.ActionCreateRun)
	wsVars := auth.RequireWorkspaceAction(s.authz, auth.ActionManageVars)
	wsReveal := auth.RequireWorkspaceAction(s.authz, auth.ActionRevealSecret)
	wsState := auth.RequireWorkspaceAction(s.authz, auth.ActionManageState)
	wsDelete := auth.RequireWorkspaceAction(s.authz, auth.ActionDeleteWorkspace)
	wsOperator := auth.RequireWorkspaceRole(s.authz, "operator")

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Config upload route (50 MB limit, separate from default 1 MB)
		r.Group(func(r chi.Router) {
			r.Use(BodySizeLimit(50 << 20))
			r.Use(authMiddleware.Authenticate)
			r.With(wsOperator).Post("/workspaces/{workspaceID}/upload", workspaceHandler.Upload)
		})

		// All other routes (1 MB limit)
		r.Group(func(r chi.Router) {
			r.Use(BodySizeLimit(1 << 20))

			r.Get("/health", healthHandler.Check)

			// Auth routes. /auth/handoff sits outside the authenticated group
			// on purpose: the caller has no bearer token yet — the endpoint IS
			// how the SPA obtains it (one-time HttpOnly-cookie exchange).
			r.Get("/auth/github", authHandler.GitHubLogin)
			r.Get("/auth/github/callback", authHandler.GitHubCallback)
			r.Post("/auth/handoff", authHandler.Handoff)
			if s.cfg.Environment == "development" {
				r.Get("/auth/dev", authHandler.DevLogin)
			}

			// VCS webhooks (public, HMAC-verified)
			r.Post("/webhooks/github", webhookHandler.GitHubPush)

			// Protected routes
			r.Group(func(r chi.Router) {
				r.Use(authMiddleware.Authenticate)

				r.Get("/auth/me", authHandler.Me)

				// Users (admin-only)
				r.Route("/users", func(r chi.Router) {
					r.With(auth.RequireRole("admin")).Get("/", userHandler.List)
					r.With(auth.RequireAction(auth.ActionManageOrg)).Put("/{userID}/role", userHandler.UpdateRole)
				})

				// Audit logs (admin-only)
				r.With(auth.RequireRole("admin")).Get("/audit-logs", auditHandler.List)

				// Org variables. The list returns keys with values redacted, so
				// it sits at the read baseline rather than with the writes.
				r.Route("/variables", func(r chi.Router) {
					r.With(auth.RequireRole("viewer")).Get("/", orgVarHandler.List)
					r.With(auth.RequireAction(auth.ActionManageVars)).Post("/", orgVarHandler.Create)
					r.Route("/{variableID}", func(r chi.Router) {
						r.With(auth.RequireAction(auth.ActionManageVars)).Put("/", orgVarHandler.Update)
						r.With(auth.RequireAction(auth.ActionManageVars)).Delete("/", orgVarHandler.Delete)
						r.With(auth.RequireAction(auth.ActionRevealSecret)).Get("/value", orgVarHandler.RevealValue)
					})
				})

				// Teams. Reads stay at the baseline: the team list is how the
				// tenant create form populates its owning-team picker for
				// operators, and the members list is a normal page for every
				// role.
				r.Route("/teams", func(r chi.Router) {
					r.With(auth.RequireRole("viewer")).Get("/", teamHandler.List)
					r.With(auth.RequireAction(auth.ActionManageTeams)).Post("/", teamHandler.Create)
					r.Route("/{teamID}", func(r chi.Router) {
						r.With(auth.RequireRole("viewer")).Get("/", teamHandler.Get)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Delete("/", teamHandler.Delete)
						r.With(auth.RequireRole("viewer")).Get("/members", teamHandler.ListMembers)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Post("/members", teamHandler.AddMember)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Put("/members/{userID}", teamHandler.UpdateMember)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Delete("/members/{userID}", teamHandler.RemoveMember)
					})
				})

				// Accounts (AWS account + assume-role config, admin-managed).
				// Reads carry the stored assume-role ARN and AWS account id, so
				// they sit at the same bar as the writes.
				r.Route("/accounts", func(r chi.Router) {
					r.With(auth.RequireRole("admin")).Get("/", accountHandler.List)
					r.With(auth.RequireRole("admin")).Post("/", accountHandler.Create)
					r.Route("/{accountID}", func(r chi.Router) {
						r.With(auth.RequireRole("admin")).Get("/", accountHandler.Get)
						r.With(auth.RequireRole("admin")).Put("/", accountHandler.Update)
						r.With(auth.RequireRole("admin")).Delete("/", accountHandler.Delete)
					})
				})

				// Clusters (Kubernetes clusters portal watches, admin-managed)
				r.Route("/clusters", func(r chi.Router) {
					r.Get("/", clusterHandler.List)
					r.With(auth.RequireRole("admin")).Post("/", clusterHandler.Create)
					r.Route("/{clusterID}", func(r chi.Router) {
						r.Get("/", clusterHandler.Get)
						r.With(auth.RequireRole("admin")).Put("/", clusterHandler.Update)
						r.With(auth.RequireRole("admin")).Delete("/", clusterHandler.Delete)
						r.With(auth.RequireRole("admin")).Post("/test-connection", clusterHandler.TestConnection)
					})
				})

				// Cluster vend order desk: provision/deprovision EKS clusters by
				// committing eks-fleet Cluster CRs to the clusters GitOps repo.
				// Reads project the same cluster_operations rows as the ops
				// feed, so they carry the ops feed's admin bar.
				r.Route("/cluster-orders", func(r chi.Router) {
					r.With(auth.RequireRole("admin")).Get("/", clusterOrderHandler.List)
					r.With(auth.RequireRole("admin")).Post("/", clusterOrderHandler.Provision)
					r.Route("/{environment}/{name}", func(r chi.Router) {
						r.With(auth.RequireRole("admin")).Get("/operations", clusterOrderHandler.Operations)
						r.With(auth.RequireRole("admin")).Delete("/", clusterOrderHandler.Deprovision)
						// Break-glass: deletes real cloud resources outside the GitOps
						// teardown, so owner-only — one tier above deprovision.
						r.With(auth.RequireRole("owner")).Post("/unwedge", clusterOrderHandler.Unwedge)
					})
				})

				// Operations feed: org-wide cluster vends + tenant deploys merged
				// into one activity stream. Admin-gated — it spans every cluster
				// and tenant in the org (the operations daily driver's home).
				r.With(auth.RequireRole("admin")).Get("/ops/feed", opsHandler.Feed)

				// Templates: admin-curated tenant flavors. Reads filter by
				// team access for non-admins; writes + access management
				// are admin-only.
				r.Route("/templates", func(r chi.Router) {
					r.Get("/", templateHandler.List)
					r.With(auth.RequireRole("admin")).Post("/", templateHandler.Create)
					r.Route("/{templateID}", func(r chi.Router) {
						r.Get("/", templateHandler.Get)
						r.With(auth.RequireRole("admin")).Put("/", templateHandler.Update)
						r.With(auth.RequireRole("admin")).Delete("/", templateHandler.Delete)
						r.Get("/access", templateHandler.ListAccess)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Post("/access", templateHandler.GrantAccess)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Delete("/access/{teamID}", templateHandler.RevokeAccess)
					})
				})

				// Tenants. Reads expose the watcher's observed inventory
				// (filtered by team access for non-admins). Writes enqueue
				// tenant_operations + persist a team-access grant.
				r.Route("/tenants", func(r chi.Router) {
					r.Get("/", tenantHandler.List)
					r.With(auth.RequireRole("operator")).Post("/", tenantHandler.Create)
					r.Route("/{tenantID}", func(r chi.Router) {
						r.Get("/", tenantHandler.Get)
						r.With(auth.RequireRole("operator")).Delete("/", tenantHandler.Delete)
						r.Get("/operations", tenantHandler.Operations)
						r.Get("/access", tenantHandler.ListAccess)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Post("/access", tenantHandler.GrantAccess)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Delete("/access/{teamID}", tenantHandler.RevokeAccess)
					})
				})

				// Pipelines
				r.Route("/pipelines", func(r chi.Router) {
					r.Get("/", pipelineHandler.List)
					r.With(auth.RequireRole("operator")).Post("/", pipelineHandler.Create)
					r.Route("/{pipelineID}", func(r chi.Router) {
						r.Get("/", pipelineHandler.Get)
						r.With(auth.RequireRole("operator")).Put("/", pipelineHandler.Update)
						r.With(auth.RequireRole("admin")).Delete("/", pipelineHandler.Delete)

						r.Route("/runs", func(r chi.Router) {
							r.Get("/", pipelineHandler.ListRuns)
							// Every stage creates its run with an auto-apply
							// override, so starting a pipeline run applies for
							// real. It carries the apply bar, not the
							// create-run baseline, and stays pinned to it if
							// that bar ever moves.
							r.With(auth.RequireAction(auth.ActionApplyRun)).Post("/", pipelineHandler.StartRun)
							r.Route("/{runId}", func(r chi.Router) {
								r.Get("/", pipelineHandler.GetRun)
								r.With(auth.RequireRole("operator")).Post("/cancel", pipelineHandler.CancelRun)
							})
						})

						// Pipeline variables
						r.Route("/variables", func(r chi.Router) {
							r.Get("/", pipelineVarHandler.List)
							r.With(auth.RequireAction(auth.ActionManageVars)).Post("/", pipelineVarHandler.Create)
							r.Route("/{variableID}", func(r chi.Router) {
								r.With(auth.RequireAction(auth.ActionManageVars)).Put("/", pipelineVarHandler.Update)
								r.With(auth.RequireAction(auth.ActionManageVars)).Delete("/", pipelineVarHandler.Delete)
								r.With(auth.RequireAction(auth.ActionRevealSecret)).Get("/value", pipelineVarHandler.RevealValue)
							})
						})
					})
				})

				// Workspaces. Everything under /{workspaceID} runs through a
				// workspace-scoped gate, so a workspace_team_access grant can
				// raise what a team may do on that one workspace.
				r.Route("/workspaces", func(r chi.Router) {
					r.With(auth.RequireAction(auth.ActionViewWorkspace)).Get("/", workspaceHandler.List)
					// No workspace exists yet, so there is nothing to grant
					// against: creation is an org-role decision, at the same
					// bar as cloning an existing one.
					r.With(auth.RequireRole("operator")).Post("/", workspaceHandler.Create)
					r.Route("/{workspaceID}", func(r chi.Router) {
						r.With(wsView).Get("/", workspaceHandler.Get)
						// Settings drive what the worker checks out and runs.
						// The two fields that decide whether an apply needs a
						// human — auto_apply and requires_approval — are held
						// to the org-level approval bar inside the handler, and
						// so is repointing the last gated workspace off a
						// configuration, which leaves that configuration open
						// to an ungated one just as surely.
						r.With(wsOperator).Put("/", workspaceHandler.Update)
						// Deleting the last workspace requiring approval on a
						// configuration leaves that configuration open to an
						// ungated one, exactly as repointing it away does, so
						// the handler holds that case at the same org-level
						// approval bar. The route itself is workspace scoped,
						// which is what makes the difference matter: an admin
						// grant on one workspace clears this gate, and it must
						// not also retire a production approval.
						r.With(wsDelete).Delete("/", workspaceHandler.Delete)
						r.With(wsOperator).Post("/lock", workspaceHandler.Lock)
						r.With(wsOperator).Post("/unlock", workspaceHandler.Unlock)
						r.With(wsOperator).Post("/clone", workspaceHandler.Clone)

						// Variables. Reads redact sensitive values; writes feed
						// the worker's tfvars file and process environment, so
						// they carry ActionManageVars — the same bar org
						// variables already sit at.
						r.Route("/variables", func(r chi.Router) {
							r.With(wsView).Get("/", variableHandler.List)
							r.With(wsView).Get("/effective", variableHandler.Effective)
							r.With(wsVars).Post("/", variableHandler.Create)
							// Discover parses the workspace's own config and returns
							// the variable names it declares. Nothing is written, and
							// below ActionManageVars nothing is valued either — the
							// handler strips the value column and skips the terragrunt
							// render that would resolve it. That leaves a read of the
							// config's shape, which is what the read bar is for: same
							// split the state routes below make, where the resource
							// inventory is readable and the attribute values are not.
							// It is a POST only because acquiring the config is the
							// expensive part.
							r.With(wsView).Post("/discover", variableHandler.Discover)
							r.With(wsVars).Post("/bulk", variableHandler.BulkCreate)
							r.With(wsVars).Post("/import-outputs", variableHandler.ImportOutputs)
							r.With(wsVars).Post("/copy", variableHandler.CopyVariables)
							r.Route("/{variableID}", func(r chi.Router) {
								r.With(wsVars).Put("/", variableHandler.Update)
								r.With(wsVars).Delete("/", variableHandler.Delete)
								r.With(wsReveal).Get("/value", variableHandler.RevealValue)
							})
						})

						// State versions. The parsed views back the State and
						// Outputs tabs; the raw download hands over the whole
						// tfstate file, every provider credential in it
						// included, so it sits with state management.
						//
						// The parsed views are on the read bar for the
						// INVENTORY only — addresses, providers, serials,
						// which attributes changed. Attribute values are the
						// same bytes the download refuses to hand over at this
						// tier (tofu writes random_password.result and
						// tls_private_key.private_key_pem into state in
						// cleartext), so handler.attributeView withholds them
						// below ActionManageState and the two routes disclose
						// the same material at the same bar.
						r.Route("/state", func(r chi.Router) {
							r.With(wsView).Get("/", stateHandler.List)
							r.With(wsView).Get("/current", stateHandler.GetCurrent)
							r.With(wsView).Get("/current/resources", stateHandler.Resources)
							r.With(wsView).Get("/current/outputs", stateHandler.Outputs)
							r.With(wsView).Get("/diff", stateHandler.Diff)
							r.With(wsView).Get("/{stateID}", stateHandler.Get)
							r.With(wsState).Get("/{stateID}/download", stateHandler.Download)
							r.With(wsState).Delete("/serial/{serial}", stateHandler.Delete)
						})

						// Team access. Handing a team rights on a workspace
						// stays an org-admin act — deliberately not workspace
						// scoped, so holding a grant never lets someone widen
						// it or pass it on.
						r.With(wsView).Get("/access", teamHandler.ListWorkspaceAccess)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Post("/access", teamHandler.SetWorkspaceAccess)
						r.With(auth.RequireAction(auth.ActionManageTeams)).Delete("/access/{teamID}", teamHandler.RemoveWorkspaceAccess)

						// Runs
						r.Route("/runs", func(r chi.Router) {
							r.With(wsView).Get("/", runHandler.List)
							// Baseline: only operator+ may create any run; the handler
							// elevates per operation (destroy requires admin).
							r.With(wsRun).Post("/", runHandler.Create)
							r.Route("/{runID}", func(r chi.Router) {
								r.With(wsView).Get("/", runHandler.Get)
								r.With(wsView).Get("/plan-json", runHandler.GetPlanJSON)
								r.With(wsView).Get("/logs/ws", runHandler.StreamLogs)
								r.With(wsOperator).Post("/cancel", runHandler.Cancel)

								// Approvals
								r.With(wsView).Get("/approvals", s.approvalHandler.List)
								// Approving releases a gated (typically prod) apply — same bar
								// as ActionApplyProd. Without this a viewer could self-approve
								// and trigger a real tofu apply. Deliberately org-scoped: the
								// authority to sign off a production apply is not something a
								// per-workspace grant can hand out.
								r.With(auth.RequireAction(auth.ActionApplyProd)).
									Post("/approvals", s.approvalHandler.Create)
							})
						})
					})
				})
			})
		})
	})

	s.router = r
}

func (s *Server) Start() error {
	s.logger.Info("starting server", "addr", s.cfg.ServerAddr)
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
