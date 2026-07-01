package handler

import (
	"net/http"
	"time"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

// OpsHandler serves the org-wide operations feed — the operations daily driver's
// at-a-glance view of cluster vends and tenant deploys across the org. Read-only;
// admin-gated at the router (it spans every cluster + tenant in the org).
type OpsHandler struct {
	svc *service.OpsFeedService
}

func NewOpsHandler(svc *service.OpsFeedService) *OpsHandler {
	return &OpsHandler{svc: svc}
}

// OpsFeedItemResponse is one entry in the merged feed: exactly one of
// Cluster/Tenant is set, discriminated by Kind.
type OpsFeedItemResponse struct {
	Kind    string                    `json:"kind"` // "cluster" | "tenant"
	At      time.Time                 `json:"at"`
	Cluster *ClusterOperationResponse `json:"cluster,omitempty"`
	Tenant  *TenantOperationResponse  `json:"tenant,omitempty"`
}

func opsFeedItemResponse(item service.OpsFeedItem) OpsFeedItemResponse {
	out := OpsFeedItemResponse{Kind: item.Kind, At: item.At}
	if item.Cluster != nil {
		c := clusterOperationResponse(*item.Cluster)
		out.Cluster = &c
	}
	if item.Tenant != nil {
		t := tenantOperationResponse(*item.Tenant)
		out.Tenant = &t
	}
	return out
}

// Feed returns the merged, recency-sorted cluster + tenant operations for the org.
func (h *OpsHandler) Feed(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	items, err := h.svc.Feed(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to load operations feed")
		return
	}
	data := make([]OpsFeedItemResponse, len(items))
	for i, item := range items {
		data[i] = opsFeedItemResponse(item)
	}
	respond.List(w, data)
}
