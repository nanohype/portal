package handler

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/vcs"
)

type WebhookHandler struct {
	queries       *repository.Queries
	runSvc        *service.RunService
	auditSvc      *service.AuditService
	webhookSecret string
}

func NewWebhookHandler(queries *repository.Queries, runSvc *service.RunService, auditSvc *service.AuditService, webhookSecret string) *WebhookHandler {
	return &WebhookHandler{
		queries:       queries,
		runSvc:        runSvc,
		auditSvc:      auditSvc,
		webhookSecret: webhookSecret,
	}
}

func (h *WebhookHandler) GitHubPush(w http.ResponseWriter, r *http.Request) {
	if h.webhookSecret == "" {
		respond.Error(w, http.StatusServiceUnavailable, "webhooks not configured")
		return
	}

	// Read body (limited by BodySizeLimit middleware)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Verify HMAC signature
	signature := r.Header.Get("X-Hub-Signature-256")
	if !vcs.VerifySignature(body, signature, h.webhookSecret) {
		slog.Warn("webhook signature verification failed",
			"remote_addr", r.RemoteAddr,
			"user_agent", r.Header.Get("User-Agent"),
			"delivery_id", r.Header.Get("X-GitHub-Delivery"),
		)
		respond.Error(w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}

	// Only handle push events
	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "ping" {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	if eventType != "push" {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ignored", "event": eventType})
		return
	}

	// Parse push event
	event, err := vcs.ParsePushEvent(body)
	if err != nil {
		slog.Warn("ignoring non-branch push event", "error", err)
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": err.Error()})
		return
	}

	branch := event.Branch()
	logger := slog.With("repo", event.RepoURL, "branch", branch, "commit", event.CommitSHA, "sender", event.SenderName)
	logger.Info("received push webhook")

	// Find matching workspaces
	// Workspaces store repo_url which may or may not have .git suffix,
	// so we normalize both sides for matching
	workspaces, err := h.queries.FindWorkspacesByRepo(r.Context(), repository.FindWorkspacesByRepoParams{
		RepoURL:    event.RepoURL,
		RepoBranch: branch,
	})
	if err != nil {
		logger.Error("failed to find matching workspaces", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to look up workspaces")
		return
	}

	if len(workspaces) == 0 {
		logger.Info("no matching workspaces found")
		respond.JSON(w, http.StatusOK, map[string]any{"status": "ok", "triggered": 0})
		return
	}

	// Trigger plan runs for each matching workspace
	triggered := 0
	for _, ws := range workspaces {
		// Check for duplicate commit to avoid creating redundant runs
		if event.CommitSHA != "" {
			exists, err := h.queries.HasRecentRunForCommit(r.Context(), repository.HasRecentRunForCommitParams{
				WorkspaceID: ws.ID,
				CommitSHA:   event.CommitSHA,
			})
			if err != nil {
				logger.Error("failed to check for existing run", "workspace_id", ws.ID, "error", err)
			} else if exists {
				logger.Info("skipping duplicate commit", "workspace_id", ws.ID, "commit", event.CommitSHA)
				continue
			}
		}

		run, err := h.runSvc.Create(r.Context(), service.CreateRunParams{
			WorkspaceID: ws.ID,
			OrgID:       ws.OrgID,
			Operation:   "plan",
			CreatedBy:   ws.CreatedBy,
			CommitSHA:   event.CommitSHA,
		})
		if err != nil {
			logger.Error("failed to create run for workspace", "workspace_id", ws.ID, "error", err)
			continue
		}

		h.auditSvc.Log(r.Context(), service.AuditEntry{
			OrgID: ws.OrgID, UserID: ws.CreatedBy,
			Action: "run.vcs_trigger", EntityType: "run", EntityID: run.ID,
			After: map[string]string{
				"commit":    event.CommitSHA,
				"branch":    branch,
				"sender":    event.SenderName,
				"workspace": ws.Name,
			},
			IPAddress: r.RemoteAddr, UserAgent: r.Header.Get("User-Agent"),
		})

		logger.Info("triggered plan run", "workspace_id", ws.ID, "workspace_name", ws.Name, "run_id", run.ID)
		triggered++
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"triggered": triggered,
	})
}
