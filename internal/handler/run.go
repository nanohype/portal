package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/service"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/worker"
)

type RunHandler struct {
	svc            *service.RunService
	workspaceSvc   *service.WorkspaceService
	streamer       logstream.Streamer
	auditSvc       *service.AuditService
	allowedOrigins []string
	storage        *storage.S3Storage
}

func NewRunHandler(svc *service.RunService, workspaceSvc *service.WorkspaceService, streamer logstream.Streamer, auditSvc *service.AuditService, allowedOrigins []string, store *storage.S3Storage) *RunHandler {
	return &RunHandler{svc: svc, workspaceSvc: workspaceSvc, streamer: streamer, auditSvc: auditSvc, allowedOrigins: allowedOrigins, storage: store}
}

// wsOriginPatterns converts full URLs to host patterns for websocket origin checking.
func wsOriginPatterns(origins []string) []string {
	patterns := make([]string, 0, len(origins))
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			patterns = append(patterns, u.Host)
		}
	}
	if len(patterns) == 0 {
		patterns = append(patterns, "localhost:*")
	}
	return patterns
}

type ImportResourceRequest struct {
	Address string `json:"address"`
	ID      string `json:"id"`
}

type CreateRunRequest struct {
	Operation string                  `json:"operation"`
	Imports   []ImportResourceRequest `json:"imports,omitempty"`
}

func (h *RunHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	runs, total, err := h.svc.List(r.Context(), workspaceID, userCtx.OrgID, page, perPage)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list runs")
		return
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[any]{
		Data:    runs,
		Total:   total,
		Page:    page,
		PerPage: perPage,
	})
}

func (h *RunHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	run, err := h.svc.Get(r.Context(), runID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "run not found")
		return
	}

	respond.JSON(w, http.StatusOK, run)
}

func (h *RunHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req CreateRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Operation == "" {
		req.Operation = "plan"
	}

	if !isValidOperation(req.Operation) {
		respond.Error(w, http.StatusBadRequest, "operation must be 'plan', 'apply', 'destroy', 'import', or 'test'")
		return
	}

	if req.Operation == "import" && len(req.Imports) == 0 {
		respond.Error(w, http.StatusBadRequest, "imports array is required for import operation")
		return
	}

	// Check if workspace is locked
	ws, err := h.workspaceSvc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Locked {
		respond.Error(w, http.StatusConflict, "workspace is locked")
		return
	}

	var imports []worker.ImportResource
	for _, imp := range req.Imports {
		imports = append(imports, worker.ImportResource{Address: imp.Address, ID: imp.ID})
	}

	run, err := h.svc.Create(r.Context(), service.CreateRunParams{
		WorkspaceID: workspaceID,
		OrgID:       userCtx.OrgID,
		Operation:   req.Operation,
		CreatedBy:   userCtx.UserID,
		Imports:     imports,
	})
	if err != nil {
		slog.Error("failed to create run", "error", err)
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create run")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "run.create", EntityType: "run", EntityID: run.ID,
		After: run, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, run)
}

// isValidOperation returns whether an operation string is valid.
func isValidOperation(op string) bool {
	switch op {
	case "plan", "apply", "destroy", "import", "test":
		return true
	default:
		return false
	}
}

// isCancellableStatus returns whether a run in the given status can be cancelled.
// "planned" is cancellable because it parks a workspace's queue indefinitely
// (the queue gate treats planned as active), and the user may decide the plan
// is stale rather than ever applying it.
func isCancellableStatus(status string) bool {
	switch status {
	case "pending", "queued", "planning", "planned", "applying", "awaiting_approval":
		return true
	default:
		return false
	}
}

func (h *RunHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	cancelled, err := h.svc.Cancel(r.Context(), runID, userCtx.OrgID)
	if err != nil {
		// CancelRun returns ErrNoRows if the run doesn't exist or isn't in a cancellable state
		respond.Error(w, http.StatusConflict, "run not found or cannot be cancelled in its current state")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "run.cancel", EntityType: "run", EntityID: runID,
		After: cancelled, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, cancelled)
}

func (h *RunHandler) GetPlanJSON(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	run, err := h.svc.Get(r.Context(), runID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "run not found")
		return
	}

	if run.PlanJSONURL == "" {
		respond.Error(w, http.StatusNotFound, "no plan JSON available")
		return
	}

	if h.storage == nil {
		respond.Error(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}

	data, err := h.storage.GetPlanJSON(r.Context(), run.PlanJSONURL)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to fetch plan JSON")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (h *RunHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: wsOriginPatterns(h.allowedOrigins),
	})
	if err != nil {
		slog.Error("websocket accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	// Use a detached context so the WebSocket isn't killed by http.Server WriteTimeout.
	// conn.CloseRead returns a context that's cancelled when the client disconnects.
	ctx := conn.CloseRead(context.Background())

	ch := h.streamer.Subscribe(runID)
	defer h.streamer.Unsubscribe(runID, ch)

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case msg, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "stream ended")
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}
