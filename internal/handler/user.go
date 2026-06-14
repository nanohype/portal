package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type UserHandler struct {
	queries  *repository.Queries
	auditSvc *service.AuditService
}

func NewUserHandler(queries *repository.Queries, auditSvc *service.AuditService) *UserHandler {
	return &UserHandler{queries: queries, auditSvc: auditSvc}
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	users, err := h.queries.ListUsersByOrg(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list users")
		return
	}

	respond.JSON(w, http.StatusOK, users)
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

	// Load the target scoped to the caller's org. A cross-org target must 404
	// (without this, an owner in org A could re-role any user in org B by ID);
	// the owner-count guard below also needs the target's current role.
	targetUser, err := h.queries.GetUser(r.Context(), targetUserID)
	if err != nil || targetUser.OrgID != userCtx.OrgID {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}

	// Prevent demoting the last owner
	if req.Role != "owner" && targetUser.Role == "owner" {
		ownerCount, err := h.queries.CountOwnersByOrg(r.Context(), userCtx.OrgID)
		if err != nil {
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to check owner count")
			return
		}
		if ownerCount <= 1 {
			respond.Error(w, http.StatusBadRequest, "cannot demote the last owner")
			return
		}
	}

	updated, err := h.queries.UpdateUserRole(r.Context(), repository.UpdateUserRoleParams{
		ID:    targetUserID,
		Role:  req.Role,
		OrgID: userCtx.OrgID,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update role")
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
