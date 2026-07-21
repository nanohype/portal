package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/repository"
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

// RunResponse projects repository.Run for API + audit consumption.
type RunResponse struct {
	ID               string     `json:"id"`
	WorkspaceID      string     `json:"workspace_id"`
	OrgID            string     `json:"org_id"`
	Operation        string     `json:"operation"`
	Status           string     `json:"status"`
	PlanOutput       string     `json:"plan_output"`
	PlanLogURL       string     `json:"plan_log_url"`
	ApplyLogURL      string     `json:"apply_log_url"`
	ResourcesAdded   int32      `json:"resources_added"`
	ResourcesChanged int32      `json:"resources_changed"`
	ResourcesDeleted int32      `json:"resources_deleted"`
	ErrorMessage     string     `json:"error_message"`
	CommitSHA        string     `json:"commit_sha"`
	PlanJSONURL      string     `json:"plan_json_url"`
	CreatedBy        string     `json:"created_by"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func runResponse(r repository.Run) RunResponse {
	return RunResponse{
		ID:               r.ID,
		WorkspaceID:      r.WorkspaceID,
		OrgID:            r.OrgID,
		Operation:        r.Operation,
		Status:           r.Status,
		PlanOutput:       r.PlanOutput,
		PlanLogURL:       r.PlanLogURL,
		ApplyLogURL:      r.ApplyLogURL,
		ResourcesAdded:   r.ResourcesAdded,
		ResourcesChanged: r.ResourcesChanged,
		ResourcesDeleted: r.ResourcesDeleted,
		ErrorMessage:     r.ErrorMessage,
		CommitSHA:        r.CommitSHA,
		PlanJSONURL:      r.PlanJSONURL,
		CreatedBy:        r.CreatedBy,
		StartedAt:        r.StartedAt,
		FinishedAt:       r.FinishedAt,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
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

	data := make([]RunResponse, len(runs))
	for i, run := range runs {
		data[i] = runResponse(run)
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[RunResponse]{
		Data:    data,
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
		respond.FromError(w, r, err)
		return
	}

	respond.JSON(w, http.StatusOK, runResponse(run))
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

	// Enforce role per operation: the route gates the create_run baseline, but
	// apply/destroy run real tofu against live cloud state, so they elevate
	// (apply -> apply_run, destroy -> destroy_run/admin). Without this a viewer
	// could POST {operation: "destroy"} and tear down infrastructure.
	//
	// The role comes from the workspace gate that already ran, so a team
	// granted a higher role on this workspace elevates here too. An empty
	// value means no gate resolved a role for this request, which denies.
	effectiveRole := auth.WorkspaceRole(r.Context())
	if userCtx == nil || effectiveRole == "" || !auth.CanPerform(effectiveRole, auth.ActionForOperation(req.Operation)) {
		respond.Error(w, http.StatusForbidden, "insufficient role for "+req.Operation+" operation")
		return
	}

	// Check if workspace is locked
	ws, err := h.workspaceSvc.Get(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
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
		After: runResponse(run), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, runResponse(run))
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
		After: runResponse(cancelled), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, runResponse(cancelled))
}

func (h *RunHandler) GetPlanJSON(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	run, err := h.svc.Get(r.Context(), runID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
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

	// Org-scope before upgrading: the run must belong to the caller's org, or
	// any authenticated user could stream any run's live logs by guessing a ULID.
	userCtx := auth.GetUser(r.Context())
	if userCtx == nil {
		respond.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if _, err := h.svc.Get(r.Context(), runID, userCtx.OrgID); err != nil {
		respond.FromError(w, r, err)
		return
	}

	// Clients authenticate by requesting subprotocols ["bearer", <jwt>]
	// (validated by auth.Middleware); offering "bearer" here echoes it back
	// as the selected subprotocol, which browsers require to complete the
	// handshake when protocols were requested.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: wsOriginPatterns(h.allowedOrigins),
		Subprotocols:   []string{auth.WebSocketBearerProtocol},
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
