package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type UserHandler struct {
	svc      *service.UserService
	auditSvc *service.AuditService
}

func NewUserHandler(svc *service.UserService, auditSvc *service.AuditService) *UserHandler {
	return &UserHandler{svc: svc, auditSvc: auditSvc}
}

// UserResponse projects repository.User for API + audit consumption.
type UserResponse struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	AvatarURL   string    `json:"avatar_url"`
	GithubID    *int64    `json:"github_id"`
	Role        string    `json:"role"`
	LastLoginAt time.Time `json:"last_login_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func userResponse(u repository.User) UserResponse {
	return UserResponse{
		ID:          u.ID,
		OrgID:       u.OrgID,
		Email:       u.Email,
		Name:        u.Name,
		AvatarURL:   u.AvatarURL,
		GithubID:    u.GithubID,
		Role:        u.Role,
		LastLoginAt: u.LastLoginAt,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}

func userResponses(users []repository.User) []UserResponse {
	out := make([]UserResponse, len(users))
	for i, u := range users {
		out[i] = userResponse(u)
	}
	return out
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	users, err := h.svc.List(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.List(w, userResponses(users))
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
		After: userResponse(updated), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, userResponse(updated))
}
