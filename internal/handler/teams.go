package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

type TeamHandler struct {
	svc      *service.TeamService
	auditSvc *service.AuditService
}

func NewTeamHandler(svc *service.TeamService, auditSvc *service.AuditService) *TeamHandler {
	return &TeamHandler{svc: svc, auditSvc: auditSvc}
}

type CreateTeamRequest struct {
	Name string `json:"name"`
}

type AddTeamMemberRequest struct {
	UserID        string `json:"user_id"`
	Role          string `json:"role"`
	CloudIdentity string `json:"cloud_identity"`
}

type UpdateTeamMemberRequest struct {
	Role          string `json:"role"`
	CloudIdentity string `json:"cloud_identity"`
}

type SetWorkspaceAccessRequest struct {
	TeamID string `json:"team_id"`
	Role   string `json:"role"`
}

func isValidRole(role string) bool {
	switch role {
	case "owner", "admin", "operator", "viewer":
		return true
	default:
		return false
	}
}

func (h *TeamHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	// `member_of=me` scopes the result to teams the caller belongs to.
	// Used by the tenant create form so operators see only teams they
	// can actually own a tenant under.
	if r.URL.Query().Get("member_of") == "me" {
		teams, err := h.svc.ListForUser(r.Context(), userCtx.UserID, userCtx.OrgID)
		if err != nil {
			respond.FromError(w, r, err)
			return
		}
		respond.List(w, teams)
		return
	}

	teams, err := h.svc.List(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.List(w, teams)
}

func (h *TeamHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		respond.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}

	team, err := h.svc.Create(r.Context(), userCtx.OrgID, req.Name)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "team.create", EntityType: "team", EntityID: team.ID,
		After: team, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, team)
}

func (h *TeamHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")

	team, err := h.svc.Get(r.Context(), teamID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.JSON(w, http.StatusOK, team)
}

func (h *TeamHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")

	if err := h.svc.Delete(r.Context(), teamID, userCtx.OrgID); err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "team.delete", EntityType: "team", EntityID: teamID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

func (h *TeamHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")

	members, err := h.svc.ListMembers(r.Context(), teamID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.List(w, members)
}

func (h *TeamHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")

	var req AddTeamMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.UserID == "" || req.Role == "" {
		respond.Error(w, http.StatusBadRequest, "user_id and role are required")
		return
	}

	if !isValidRole(req.Role) {
		respond.Error(w, http.StatusBadRequest, "role must be 'owner', 'admin', 'operator', or 'viewer'")
		return
	}

	member, err := h.svc.AddMember(r.Context(), service.AddTeamMemberParams{
		TeamID:        teamID,
		OrgID:         userCtx.OrgID,
		UserID:        req.UserID,
		Role:          req.Role,
		CloudIdentity: req.CloudIdentity,
	})
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "team.add_member", EntityType: "team_member", EntityID: member.ID,
		After: member, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, member)
}

func (h *TeamHandler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")
	userID := chi.URLParam(r, "userID")

	var req UpdateTeamMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Role == "" {
		respond.Error(w, http.StatusBadRequest, "role is required")
		return
	}

	if !isValidRole(req.Role) {
		respond.Error(w, http.StatusBadRequest, "role must be 'owner', 'admin', 'operator', or 'viewer'")
		return
	}

	member, err := h.svc.UpdateMember(r.Context(), service.UpdateTeamMemberParams{
		TeamID:        teamID,
		OrgID:         userCtx.OrgID,
		UserID:        userID,
		Role:          req.Role,
		CloudIdentity: req.CloudIdentity,
	})
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "team.update_member", EntityType: "team_member", EntityID: member.ID,
		After: member, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, member)
}

func (h *TeamHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")
	userID := chi.URLParam(r, "userID")

	if err := h.svc.RemoveMember(r.Context(), teamID, userCtx.OrgID, userID); err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "team.remove_member", EntityType: "team_member", EntityID: teamID + "/" + userID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

func (h *TeamHandler) ListWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	access, err := h.svc.ListWorkspaceAccess(r.Context(), workspaceID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.List(w, access)
}

func (h *TeamHandler) SetWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	var req SetWorkspaceAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.TeamID == "" || req.Role == "" {
		respond.Error(w, http.StatusBadRequest, "team_id and role are required")
		return
	}

	if !isValidRole(req.Role) {
		respond.Error(w, http.StatusBadRequest, "role must be 'owner', 'admin', 'operator', or 'viewer'")
		return
	}

	access, err := h.svc.SetWorkspaceAccess(r.Context(), service.SetWorkspaceAccessParams{
		WorkspaceID: workspaceID,
		OrgID:       userCtx.OrgID,
		TeamID:      req.TeamID,
		Role:        req.Role,
	})
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.set_team_access", EntityType: "workspace_team_access",
		EntityID: access.ID, After: access, IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, access)
}

func (h *TeamHandler) RemoveWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")
	teamID := chi.URLParam(r, "teamID")

	if err := h.svc.RemoveWorkspaceAccess(r.Context(), workspaceID, userCtx.OrgID, teamID); err != nil {
		respond.FromError(w, r, err)
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "workspace.remove_team_access", EntityType: "workspace_team_access",
		EntityID: workspaceID + "/" + teamID, IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}
