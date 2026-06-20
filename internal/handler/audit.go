package handler

import (
	"net/http"
	"strconv"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/conv"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
)

type AuditHandler struct {
	queries *repository.Queries
}

func NewAuditHandler(queries *repository.Queries) *AuditHandler {
	return &AuditHandler{queries: queries}
}

func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}

	offset := conv.Int32((page - 1) * perPage)

	logs, err := h.queries.ListAuditLogs(r.Context(), repository.ListAuditLogsParams{
		OrgID:  userCtx.OrgID,
		Limit:  int32(perPage),
		Offset: offset,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list audit logs")
		return
	}

	respond.List(w, logs)
}
