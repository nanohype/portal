package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

type ApprovalHandler struct {
	svc *service.ApprovalService
}

func NewApprovalHandler(svc *service.ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{svc: svc}
}

type ApprovalRequest struct {
	Status  string `json:"status"` // "approved" or "rejected"
	Comment string `json:"comment"`
}

func (h *ApprovalHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	approvals, err := h.svc.List(r.Context(), runID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}
	respond.List(w, approvals)
}

func (h *ApprovalHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	runID := chi.URLParam(r, "runID")

	var req ApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status != "approved" && req.Status != "rejected" {
		respond.Error(w, http.StatusBadRequest, "status must be 'approved' or 'rejected'")
		return
	}

	// IP/UA are threaded into the service so the audit row is written inside the
	// same transaction as the decision — see ApprovalService.Create.
	ip, ua := auditContext(r)
	approval, err := h.svc.Create(r.Context(), runID, userCtx.OrgID, userCtx.UserID, req.Status, req.Comment, ip, ua)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.JSON(w, http.StatusCreated, approval)
}
