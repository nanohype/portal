package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

type UserHandler struct {
	svc      *service.UserService
	auditSvc *service.AuditService
}

func NewUserHandler(svc *service.UserService, auditSvc *service.AuditService) *UserHandler {
	return &UserHandler{svc: svc, auditSvc: auditSvc}
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	users, err := h.svc.List(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.List(w, users)
}

type UpdateRoleRequest struct {
	Role string `json:"role"`
}

func (h *UserHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	targetUserID := chi.URLParam(r, "userID")

	var req UpdateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !isValidRole(req.Role) {
		respond.Error(w, http.StatusBadRequest, "role must be 'owner', 'admin', 'operator', or 'viewer'")
		return
	}

	updated, err := h.svc.UpdateRole(r.Context(), targetUserID, userCtx.OrgID, req.Role)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "user.update_role", EntityType: "user", EntityID: targetUserID,
		After: updated, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, updated)
}
