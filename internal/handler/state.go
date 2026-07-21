package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type StateHandler struct {
	svc      *service.StateService
	auditSvc *service.AuditService
}

func NewStateHandler(svc *service.StateService, auditSvc *service.AuditService) *StateHandler {
	return &StateHandler{svc: svc, auditSvc: auditSvc}
}

// StateVersionResponse projects repository.StateVersion for API + audit
// consumption.
type StateVersionResponse struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspace_id"`
	OrgID           string    `json:"org_id"`
	RunID           string    `json:"run_id"`
	Serial          int32     `json:"serial"`
	StateURL        string    `json:"state_url"`
	ResourceCount   int32     `json:"resource_count"`
	ResourceSummary string    `json:"resource_summary"`
	CreatedAt       time.Time `json:"created_at"`
}

func stateVersionResponse(sv repository.StateVersion) StateVersionResponse {
	return StateVersionResponse{
		ID:              sv.ID,
		WorkspaceID:     sv.WorkspaceID,
		OrgID:           sv.OrgID,
		RunID:           sv.RunID,
		Serial:          sv.Serial,
		StateURL:        sv.StateURL,
		ResourceCount:   sv.ResourceCount,
		ResourceSummary: sv.ResourceSummary,
		CreatedAt:       sv.CreatedAt,
	}
}

func (h *StateHandler) List(w http.ResponseWriter, r *http.Request) {
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

	versions, err := h.svc.ListVersions(r.Context(), workspaceID, userCtx.OrgID, page, perPage)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	data := make([]StateVersionResponse, len(versions))
	for i, sv := range versions {
		data[i] = stateVersionResponse(sv)
	}
	respond.List(w, data)
}

func (h *StateHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	sv, err := h.svc.Latest(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, stateVersionResponse(sv))
}

func (h *StateHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	stateID := chi.URLParam(r, "stateID")

	sv, err := h.svc.Version(r.Context(), stateID, workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, stateVersionResponse(sv))
}

func (h *StateHandler) Download(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	stateID := chi.URLParam(r, "stateID")

	// The blob is the workspace's whole tfstate. It is fetched by the workspace
	// this request was authorized against, so a state-version id from another
	// workspace is a 404.
	_, data, err := h.svc.Download(r.Context(), stateID, workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=terraform.tfstate")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (h *StateHandler) Resources(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	resources, err := h.svc.Resources(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, resources)
}

func (h *StateHandler) Outputs(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	outputs, err := h.svc.Outputs(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, outputs)
}

func (h *StateHandler) Diff(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	fromSerial, err := strconv.Atoi(r.URL.Query().Get("from"))
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid 'from' serial")
		return
	}
	toSerial, err := strconv.Atoi(r.URL.Query().Get("to"))
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid 'to' serial")
		return
	}

	diff, err := h.svc.Diff(r.Context(), workspaceID, userCtx.OrgID, fromSerial, toSerial)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.JSON(w, http.StatusOK, diff)
}

// Delete drops a single state-version row + its S3 objects. Last-ditch recovery
// for cases where the worker captured a state that's broken (e.g. encrypted with
// a passphrase that's no longer accessible, or a "partial (errored)" row from a
// half-failed apply that should be discarded so the next run rolls back to an
// earlier serial). Admin-only; the route applies the RBAC gate.
func (h *StateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	serial, err := strconv.Atoi(chi.URLParam(r, "serial"))
	if err != nil || serial < 0 {
		respond.Error(w, http.StatusBadRequest, "invalid serial")
		return
	}

	sv, storageErr, err := h.svc.DeleteVersion(r.Context(), workspaceID, userCtx.OrgID, serial)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "state_version.delete", EntityType: "state_version", EntityID: sv.ID,
		Before: stateVersionResponse(sv), IPAddress: ip, UserAgent: ua,
	})

	// Best-effort S3 cleanup already ran in the service; surface a cleanup
	// failure as a 200 with the detail rather than failing the (completed) delete.
	if storageErr != nil {
		respond.JSON(w, http.StatusOK, map[string]any{
			"deleted":       stateVersionResponse(sv),
			"storage_error": storageErr.Error(),
		})
		return
	}
	respond.NoContent(w)
}
