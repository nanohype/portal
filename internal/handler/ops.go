package handler

import (
	"net/http"

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

// Feed returns the merged, recency-sorted cluster + tenant operations for the org.
func (h *OpsHandler) Feed(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	items, err := h.svc.Feed(r.Context(), userCtx.OrgID)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to load operations feed")
		return
	}
	respond.JSON(w, http.StatusOK, items)
}
