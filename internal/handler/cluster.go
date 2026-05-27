package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type ClusterHandler struct {
	svc      *service.ClusterService
	acctSvc  *service.AccountService
	auditSvc *service.AuditService
}

func NewClusterHandler(svc *service.ClusterService, acctSvc *service.AccountService, auditSvc *service.AuditService) *ClusterHandler {
	return &ClusterHandler{svc: svc, acctSvc: acctSvc, auditSvc: auditSvc}
}

type CreateClusterRequest struct {
	AccountID   string `json:"account_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Environment string `json:"environment"`
	APIEndpoint string `json:"api_endpoint"`
	CABundle    string `json:"ca_bundle"`
	SAToken     string `json:"sa_token"`
	Region      string `json:"region"`
}

type UpdateClusterRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Environment string `json:"environment"`
	APIEndpoint string `json:"api_endpoint"`
	CABundle    string `json:"ca_bundle"`
	SAToken     string `json:"sa_token"`
	Region      string `json:"region"`
}

// ClusterResponse projects repository.Cluster for API + audit consumption.
// CABundleEncrypted + SATokenEncrypted are `json:"-"` on the embedded struct
// so the ciphertext never leaves the server; CredentialsSet lets clients
// know whether credentials are configured without seeing them.
type ClusterResponse struct {
	repository.Cluster
	CredentialsSet bool `json:"credentials_set"`
}

func clusterResponse(c repository.Cluster) ClusterResponse {
	return ClusterResponse{
		Cluster:        c,
		CredentialsSet: c.CABundleEncrypted != "" && c.SATokenEncrypted != "",
	}
}

func isValidAPIEndpoint(s string) bool {
	return strings.HasPrefix(s, "https://") && len(s) <= 2048
}

func looksLikePEM(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "-----BEGIN") && strings.Contains(t, "-----END")
}

func (h *ClusterHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	accountID := r.URL.Query().Get("account_id")

	clusters, total, err := h.svc.List(r.Context(), userCtx.OrgID, accountID, page, perPage)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list clusters")
		return
	}

	data := make([]ClusterResponse, len(clusters))
	for i, c := range clusters {
		data[i] = clusterResponse(c)
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[ClusterResponse]{
		Data: data, Total: total, Page: page, PerPage: perPage,
	})
}

func (h *ClusterHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	clusterID := chi.URLParam(r, "clusterID")

	cluster, err := h.svc.Get(r.Context(), clusterID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "cluster not found")
		return
	}
	respond.JSON(w, http.StatusOK, clusterResponse(cluster))
}

func (h *ClusterHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.AccountID = strings.TrimSpace(req.AccountID)
	req.APIEndpoint = strings.TrimSpace(req.APIEndpoint)
	req.Region = strings.TrimSpace(req.Region)

	if req.Name == "" {
		respond.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if req.AccountID == "" {
		respond.Error(w, http.StatusBadRequest, "account_id is required")
		return
	}
	// Cross-check: account must exist in caller's org.
	account, err := h.acctSvc.Get(r.Context(), req.AccountID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "account_id does not reference a known account")
		return
	}
	if req.Environment != "" && req.Environment != "development" && req.Environment != "staging" && req.Environment != "production" {
		respond.Error(w, http.StatusBadRequest, "environment must be development, staging, or production")
		return
	}
	if !isValidAPIEndpoint(req.APIEndpoint) {
		respond.Error(w, http.StatusBadRequest, "api_endpoint must be an https:// URL")
		return
	}
	if !looksLikePEM(req.CABundle) {
		respond.Error(w, http.StatusBadRequest, "ca_bundle must be a PEM-encoded certificate (-----BEGIN CERTIFICATE-----…-----END CERTIFICATE-----)")
		return
	}
	if len(req.CABundle) > 64*1024 {
		respond.Error(w, http.StatusBadRequest, "ca_bundle must be at most 64KB")
		return
	}
	if strings.TrimSpace(req.SAToken) == "" {
		respond.Error(w, http.StatusBadRequest, "sa_token is required")
		return
	}
	if len(req.SAToken) > 8*1024 {
		respond.Error(w, http.StatusBadRequest, "sa_token must be at most 8KB")
		return
	}
	// Region defaults to the parent account's region when not supplied —
	// most operators want the cluster in the same region as the account.
	region := req.Region
	if region == "" {
		region = account.DefaultRegion
	}
	if !isValidRegion(region) {
		respond.Error(w, http.StatusBadRequest, "region must look like 'us-west-2'")
		return
	}

	cluster, err := h.svc.Create(r.Context(), service.CreateClusterParams{
		OrgID:       userCtx.OrgID,
		AccountID:   req.AccountID,
		Name:        req.Name,
		Description: req.Description,
		Environment: req.Environment,
		APIEndpoint: req.APIEndpoint,
		CABundle:    req.CABundle,
		SAToken:     req.SAToken,
		Region:      region,
		CreatedBy:   userCtx.UserID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respond.Error(w, http.StatusConflict, "a cluster with this name already exists")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create cluster")
		return
	}

	// Fire-and-forget the connection test. Failure to enqueue is logged but
	// not surfaced as a 5xx — the cluster row exists, the user can re-trigger
	// the test from the UI if the job didn't fire.
	_ = h.svc.EnqueueConnectionTest(r.Context(), cluster.ID, cluster.OrgID)

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "cluster.create", EntityType: "cluster", EntityID: cluster.ID,
		After: clusterResponse(cluster), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, clusterResponse(cluster))
}

func (h *ClusterHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	clusterID := chi.URLParam(r, "clusterID")

	existing, err := h.svc.Get(r.Context(), clusterID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "cluster not found")
		return
	}

	var req UpdateClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.APIEndpoint = strings.TrimSpace(req.APIEndpoint)
	req.Region = strings.TrimSpace(req.Region)

	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if req.Environment != "" && req.Environment != "development" && req.Environment != "staging" && req.Environment != "production" {
		respond.Error(w, http.StatusBadRequest, "environment must be development, staging, or production")
		return
	}
	if req.APIEndpoint != "" && !isValidAPIEndpoint(req.APIEndpoint) {
		respond.Error(w, http.StatusBadRequest, "api_endpoint must be an https:// URL")
		return
	}
	if req.CABundle != "" {
		if !looksLikePEM(req.CABundle) {
			respond.Error(w, http.StatusBadRequest, "ca_bundle must be PEM-encoded")
			return
		}
		if len(req.CABundle) > 64*1024 {
			respond.Error(w, http.StatusBadRequest, "ca_bundle must be at most 64KB")
			return
		}
	}
	if req.SAToken != "" && len(req.SAToken) > 8*1024 {
		respond.Error(w, http.StatusBadRequest, "sa_token must be at most 8KB")
		return
	}
	if req.Region != "" && !isValidRegion(req.Region) {
		respond.Error(w, http.StatusBadRequest, "region must look like 'us-west-2'")
		return
	}

	cluster, err := h.svc.Update(r.Context(), service.UpdateClusterParams{
		ID:          clusterID,
		OrgID:       userCtx.OrgID,
		Name:        req.Name,
		Description: req.Description,
		Environment: req.Environment,
		APIEndpoint: req.APIEndpoint,
		CABundle:    req.CABundle,
		SAToken:     req.SAToken,
		Region:      req.Region,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respond.Error(w, http.StatusConflict, "a cluster with this name already exists")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update cluster")
		return
	}

	// If credentials changed, retest. (Name/description/env-only edits
	// don't change reachability.)
	if req.APIEndpoint != "" || req.CABundle != "" || req.SAToken != "" {
		_ = h.svc.EnqueueConnectionTest(r.Context(), cluster.ID, cluster.OrgID)
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "cluster.update", EntityType: "cluster", EntityID: clusterID,
		Before: clusterResponse(existing), After: clusterResponse(cluster),
		IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, clusterResponse(cluster))
}

func (h *ClusterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	clusterID := chi.URLParam(r, "clusterID")

	existing, err := h.svc.Get(r.Context(), clusterID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "cluster not found")
		return
	}

	if err := h.svc.Delete(r.Context(), clusterID, userCtx.OrgID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to delete cluster")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "cluster.delete", EntityType: "cluster", EntityID: clusterID,
		Before: clusterResponse(existing), IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}

// TestConnection re-enqueues the async connection-test job. Used when the
// operator just rotated credentials in the underlying cluster and wants to
// reverify, or wants a fresh proof-of-life after a network hiccup.
func (h *ClusterHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	clusterID := chi.URLParam(r, "clusterID")

	cluster, err := h.svc.Get(r.Context(), clusterID, userCtx.OrgID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "cluster not found")
		return
	}

	if err := h.svc.EnqueueConnectionTest(r.Context(), cluster.ID, cluster.OrgID); err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to enqueue connection test")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "cluster.test_connection", EntityType: "cluster", EntityID: clusterID,
		IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}
