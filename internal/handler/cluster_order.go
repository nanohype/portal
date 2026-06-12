package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/clusterspec"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

// ClusterOrderHandler is the vend order desk: provision/deprovision EKS clusters
// by committing eks-fleet Cluster CRs to the clusters GitOps repo. Each write
// creates a cluster_operations row the UI can show. Admin-gated at the router
// (vending a cluster is an account-level action), so unlike tenants there's no
// team-scoping here.
type ClusterOrderHandler struct {
	svc      *service.ClusterOrderService
	auditSvc *service.AuditService
}

func NewClusterOrderHandler(svc *service.ClusterOrderService, auditSvc *service.AuditService) *ClusterOrderHandler {
	return &ClusterOrderHandler{svc: svc, auditSvc: auditSvc}
}

// Provision enqueues a cluster_operation of kind=provision. The request body is
// the clusterspec.Input the operator fills in (name, account, region, team +
// optional sizing/network). The vended cluster auto-registers into the cluster
// surface once it comes up (the watch-back worker).
func (h *ClusterOrderHandler) Provision(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var in clusterspec.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Account = strings.TrimSpace(in.Account)
	in.Region = strings.TrimSpace(in.Region)
	in.Team = strings.TrimSpace(in.Team)

	// Validation is a user error (400); enqueue failure is infra (500).
	if err := in.Validate(); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	op, err := h.svc.EnqueueProvision(r.Context(), userCtx.OrgID, userCtx.UserID, in)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to enqueue cluster provision")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "cluster.provision_requested", EntityType: "cluster_operation", EntityID: op.ID,
		After: op, IPAddress: ip, UserAgent: ua,
	})
	respond.JSON(w, http.StatusAccepted, op)
}

// Deprovision enqueues a cluster_operation of kind=deprovision: the worker
// removes the Cluster CR from the clusters repo, ArgoCD prunes it, and Crossplane
// tears the cluster down (tofu destroy). name+environment come from the path; the
// team (the CR namespace) comes from the ?team= query.
func (h *ClusterOrderHandler) Deprovision(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	environment := chi.URLParam(r, "environment")
	name := chi.URLParam(r, "name")
	team := strings.TrimSpace(r.URL.Query().Get("team"))
	if team == "" {
		respond.Error(w, http.StatusBadRequest, "team query parameter is required")
		return
	}

	op, err := h.svc.EnqueueDeprovision(r.Context(), userCtx.OrgID, name, environment, team, userCtx.UserID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to enqueue cluster deprovision")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "cluster.deprovision_requested", EntityType: "cluster_operation", EntityID: op.ID,
		After: op, IPAddress: ip, UserAgent: ua,
	})
	respond.JSON(w, http.StatusAccepted, op)
}

// Operations returns the vend log for one cluster (by environment+name),
// newest first.
func (h *ClusterOrderHandler) Operations(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	environment := chi.URLParam(r, "environment")
	name := chi.URLParam(r, "name")

	ops, err := h.svc.ListOperations(r.Context(), userCtx.OrgID, name, environment)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list cluster operations")
		return
	}
	respond.JSON(w, http.StatusOK, ops)
}

// List returns recent cluster operations across the org — the Clusters-tab
// order feed, so an in-flight or failed vend is visible right after ordering.
func (h *ClusterOrderHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	ops, err := h.svc.ListAllOperations(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list cluster operations")
		return
	}
	respond.JSON(w, http.StatusOK, ops)
}
