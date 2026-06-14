package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

var slugRegex = regexp.MustCompile("[^a-z0-9-]")

type TeamHandler struct {
	queries  *repository.Queries
	auditSvc *service.AuditService
}

func NewTeamHandler(queries *repository.Queries, auditSvc *service.AuditService) *TeamHandler {
	return &TeamHandler{queries: queries, auditSvc: auditSvc}
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
		teams, err := h.queries.ListTeamsForUser(r.Context(), userCtx.UserID, userCtx.OrgID)
		if err != nil {
			respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list teams")
			return
		}
		respond.List(w, teams)
		return
	}

	teams, err := h.queries.ListTeams(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list teams")
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

	slug := slugRegex.ReplaceAllString(strings.ToLower(req.Name), "")
	slug = strings.Trim(slug, "-")
	if len(slug) > 64 {
		slug = slug[:64]
	}
	if slug == "" {
		respond.Error(w, http.StatusBadRequest, "name must contain at least one alphanumeric character")
		return
	}

	team, err := h.queries.CreateTeam(r.Context(), repository.CreateTeamParams{
		ID:    ulid.Make().String(),
		OrgID: userCtx.OrgID,
		Name:  req.Name,
		Slug:  slug,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create team")
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

	team, err := h.queries.GetTeam(r.Context(), teamID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "team not found")
		return
	}

	respond.JSON(w, http.StatusOK, team)
}

func (h *TeamHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")

	if err := h.queries.DeleteTeam(r.Context(), teamID, userCtx.OrgID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to delete team")
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

	if _, err := h.queries.GetTeam(r.Context(), teamID, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "team not found")
		return
	}

	members, err := h.queries.ListTeamMembers(r.Context(), teamID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list members")
		return
	}

	respond.List(w, members)
}

func (h *TeamHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	teamID := chi.URLParam(r, "teamID")

	if _, err := h.queries.GetTeam(r.Context(), teamID, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "team not found")
		return
	}

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

	member, err := h.queries.AddTeamMember(r.Context(), repository.AddTeamMemberParams{
		ID:            ulid.Make().String(),
		TeamID:        teamID,
		UserID:        req.UserID,
		Role:          req.Role,
		CloudIdentity: req.CloudIdentity,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to add member")
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

	if _, err := h.queries.GetTeam(r.Context(), teamID, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "team not found")
		return
	}

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

	member, err := h.queries.UpdateTeamMember(r.Context(), repository.UpdateTeamMemberParams{
		TeamID:        teamID,
		UserID:        userID,
		Role:          req.Role,
		CloudIdentity: req.CloudIdentity,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update member")
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

	if _, err := h.queries.GetTeam(r.Context(), teamID, userCtx.OrgID); err != nil {
		respond.Error(w, http.StatusNotFound, "team not found")
		return
	}

	if err := h.queries.RemoveTeamMember(r.Context(), teamID, userID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to remove member")
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

	if _, err := h.queries.GetWorkspace(r.Context(), repository.GetWorkspaceParams{
		ID: workspaceID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.Error(w, http.StatusNotFound, "workspace not found")
		return
	}

	access, err := h.queries.ListWorkspaceTeamAccess(r.Context(), workspaceID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list access")
		return
	}

	respond.List(w, access)
}

func (h *TeamHandler) SetWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	workspaceID := chi.URLParam(r, "workspaceID")

	if _, err := h.queries.GetWorkspace(r.Context(), repository.GetWorkspaceParams{
		ID: workspaceID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.Error(w, http.StatusNotFound, "workspace not found")
		return
	}

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

	access, err := h.queries.SetWorkspaceTeamAccess(r.Context(), repository.SetWorkspaceTeamAccessParams{
		ID:          ulid.Make().String(),
		WorkspaceID: workspaceID,
		TeamID:      req.TeamID,
		Role:        req.Role,
	})
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to set access")
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

	if _, err := h.queries.GetWorkspace(r.Context(), repository.GetWorkspaceParams{
		ID: workspaceID, OrgID: userCtx.OrgID,
	}); err != nil {
		respond.Error(w, http.StatusNotFound, "workspace not found")
		return
	}

	if err := h.queries.RemoveWorkspaceTeamAccess(r.Context(), workspaceID, teamID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to revoke access")
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
