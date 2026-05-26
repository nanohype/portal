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

	"github.com/stxkxs/tofui/internal/auth"
	"github.com/stxkxs/tofui/internal/domain"
	"github.com/stxkxs/tofui/internal/handler"
	"github.com/stxkxs/tofui/internal/logstream"
	"github.com/stxkxs/tofui/internal/repository"
	"github.com/stxkxs/tofui/internal/secrets"
	"github.com/stxkxs/tofui/internal/service"
	"github.com/stxkxs/tofui/internal/storage"
)

type Server struct {
	cfg             *domain.Config
	router          chi.Router
	db              *pgxpool.Pool
	logger          *slog.Logger
	http            *http.Server
	approvalHandler *handler.ApprovalHandler
	runSvc          *service.RunService
	pipelineSvc     *service.PipelineService
}

func New(cfg *domain.Config, db *pgxpool.Pool, logger *slog.Logger) *Server {
	s := &Server{
		cfg:    cfg,
		db:     db,
		logger: logger,
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

func (s *Server) ApprovalHandler() *handler.ApprovalHandler {
	return s.approvalHandler
}

func (s *Server) setupRouter() {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
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
	authMiddleware := auth.NewMiddleware(jwtAuth)

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

	authHandler := handler.NewAuthHandler(s.cfg, queries, s.db, jwtAuth)
	workspaceSvc := service.NewWorkspaceService(queries, s.db)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceSvc, auditSvc, store, queries)
	wsOrigins := []string{s.cfg.WebURL}
	if s.cfg.Environment == "development" {
		wsOrigins = append(wsOrigins, "http://localhost:5173")
	}
	runHandler := handler.NewRunHandler(s.runSvc, workspaceSvc, streamer, auditSvc, wsOrigins, store)
	variableHandler := handler.NewVariableHandler(queries, encryptor, auditSvc, workspaceSvc, store)
	teamHandler := handler.NewTeamHandler(queries, auditSvc)
	stateHandler := handler.NewStateHandler(queries, store, auditSvc)
	s.approvalHandler = handler.NewApprovalHandler(queries, s.db, auditSvc)
	auditHandler := handler.NewAuditHandler(queries)
	healthHandler := handler.NewHealthHandler(s.db, s.cfg.Environment)
	userHandler := handler.NewUserHandler(queries, auditSvc)
	orgVarHandler := handler.NewOrgVariableHandler(queries, encryptor, auditSvc)
	pipelineVarHandler := handler.NewPipelineVariableHandler(queries, encryptor, auditSvc)
	s.pipelineSvc = service.NewPipelineService(queries, s.db, s.runSvc, store)
	pipelineHandler := handler.NewPipelineHandler(s.pipelineSvc, auditSvc)
	webhookHandler := handler.NewWebhookHandler(queries, s.runSvc, auditSvc, s.cfg.WebhookSecret)

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Config upload route (50 MB limit, separate from default 1 MB)
		r.Group(func(r chi.Router) {
			r.Use(BodySizeLimit(50 << 20))
			r.Use(authMiddleware.Authenticate)
			r.With(auth.RequireRole("operator")).Post("/workspaces/{workspaceID}/upload", workspaceHandler.Upload)
		})

		// All other routes (1 MB limit)
		r.Group(func(r chi.Router) {
			r.Use(BodySizeLimit(1 << 20))

			r.Get("/health", healthHandler.Check)

			// Auth routes
			r.Get("/auth/github", authHandler.GitHubLogin)
			r.Get("/auth/github/callback", authHandler.GitHubCallback)
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
					r.With(auth.RequireRole("owner")).Put("/{userID}/role", userHandler.UpdateRole)
				})

				// Audit logs (admin-only)
				r.With(auth.RequireRole("admin")).Get("/audit-logs", auditHandler.List)

				// Org variables
				r.Route("/variables", func(r chi.Router) {
					r.Get("/", orgVarHandler.List)
					r.With(auth.RequireRole("admin")).Post("/", orgVarHandler.Create)
					r.Route("/{variableID}", func(r chi.Router) {
						r.With(auth.RequireRole("admin")).Put("/", orgVarHandler.Update)
						r.With(auth.RequireRole("admin")).Delete("/", orgVarHandler.Delete)
						r.With(auth.RequireRole("operator")).Get("/value", orgVarHandler.RevealValue)
					})
				})

				// Teams
				r.Route("/teams", func(r chi.Router) {
					r.Get("/", teamHandler.List)
					r.With(auth.RequireRole("admin")).Post("/", teamHandler.Create)
					r.Route("/{teamID}", func(r chi.Router) {
						r.Get("/", teamHandler.Get)
						r.With(auth.RequireRole("admin")).Delete("/", teamHandler.Delete)
						r.Get("/members", teamHandler.ListMembers)
						r.With(auth.RequireRole("admin")).Post("/members", teamHandler.AddMember)
						r.With(auth.RequireRole("admin")).Put("/members/{userID}", teamHandler.UpdateMember)
						r.With(auth.RequireRole("admin")).Delete("/members/{userID}", teamHandler.RemoveMember)
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
							r.With(auth.RequireRole("operator")).Post("/", pipelineHandler.StartRun)
							r.Route("/{runId}", func(r chi.Router) {
								r.Get("/", pipelineHandler.GetRun)
								r.With(auth.RequireRole("operator")).Post("/cancel", pipelineHandler.CancelRun)
							})
						})

						// Pipeline variables
						r.Route("/variables", func(r chi.Router) {
							r.Get("/", pipelineVarHandler.List)
							r.With(auth.RequireRole("operator")).Post("/", pipelineVarHandler.Create)
							r.Route("/{variableID}", func(r chi.Router) {
								r.With(auth.RequireRole("operator")).Put("/", pipelineVarHandler.Update)
								r.With(auth.RequireRole("operator")).Delete("/", pipelineVarHandler.Delete)
								r.With(auth.RequireRole("operator")).Get("/value", pipelineVarHandler.RevealValue)
							})
						})
					})
				})

				// Workspaces
				r.Route("/workspaces", func(r chi.Router) {
					r.Get("/", workspaceHandler.List)
					r.Post("/", workspaceHandler.Create)
					r.Route("/{workspaceID}", func(r chi.Router) {
						r.Get("/", workspaceHandler.Get)
						r.Put("/", workspaceHandler.Update)
						r.With(auth.RequireRole("admin")).Delete("/", workspaceHandler.Delete)
						r.With(auth.RequireRole("operator")).Post("/lock", workspaceHandler.Lock)
						r.With(auth.RequireRole("operator")).Post("/unlock", workspaceHandler.Unlock)
						r.With(auth.RequireRole("operator")).Post("/clone", workspaceHandler.Clone)

						// Variables
						r.Route("/variables", func(r chi.Router) {
							r.Get("/", variableHandler.List)
							r.Get("/effective", variableHandler.Effective)
							r.Post("/", variableHandler.Create)
							r.Post("/discover", variableHandler.Discover)
							r.Post("/bulk", variableHandler.BulkCreate)
							r.Post("/import-outputs", variableHandler.ImportOutputs)
							r.Post("/copy", variableHandler.CopyVariables)
							r.Route("/{variableID}", func(r chi.Router) {
								r.Put("/", variableHandler.Update)
								r.Delete("/", variableHandler.Delete)
								r.With(auth.RequireRole("operator")).Get("/value", variableHandler.RevealValue)
							})
						})

						// State versions
						r.Route("/state", func(r chi.Router) {
							r.Get("/", stateHandler.List)
							r.Get("/current", stateHandler.GetCurrent)
							r.Get("/current/resources", stateHandler.Resources)
							r.Get("/current/outputs", stateHandler.Outputs)
							r.Get("/diff", stateHandler.Diff)
							r.Get("/{stateID}", stateHandler.Get)
							r.Get("/{stateID}/download", stateHandler.Download)
							r.With(auth.RequireRole("admin")).Delete("/serial/{serial}", stateHandler.Delete)
						})

						// Team access
						r.Get("/access", teamHandler.ListWorkspaceAccess)
						r.With(auth.RequireRole("admin")).Post("/access", teamHandler.SetWorkspaceAccess)
						r.With(auth.RequireRole("admin")).Delete("/access/{teamID}", teamHandler.RemoveWorkspaceAccess)

						// Runs
						r.Route("/runs", func(r chi.Router) {
							r.Get("/", runHandler.List)
							r.Post("/", runHandler.Create)
							r.Route("/{runID}", func(r chi.Router) {
								r.Get("/", runHandler.Get)
								r.Get("/plan-json", runHandler.GetPlanJSON)
								r.Get("/logs/ws", runHandler.StreamLogs)
								r.With(auth.RequireRole("operator")).Post("/cancel", runHandler.Cancel)

								// Approvals
								r.Get("/approvals", s.approvalHandler.List)
								r.Post("/approvals", s.approvalHandler.Create)
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
