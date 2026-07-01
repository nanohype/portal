package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

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

// AuditLogResponse projects repository.AuditLog for API consumption.
type AuditLogResponse struct {
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id"`
	UserID     string          `json:"user_id"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	BeforeData json.RawMessage `json:"before_data"`
	AfterData  json.RawMessage `json:"after_data"`
	IPAddress  string          `json:"ip_address"`
	UserAgent  string          `json:"user_agent"`
	CreatedAt  time.Time       `json:"created_at"`
}

func auditLogResponse(l repository.AuditLog) AuditLogResponse {
	return AuditLogResponse{
		ID:         l.ID,
		OrgID:      l.OrgID,
		UserID:     l.UserID,
		Action:     l.Action,
		EntityType: l.EntityType,
		EntityID:   l.EntityID,
		BeforeData: l.BeforeData,
		AfterData:  l.AfterData,
		IPAddress:  l.IPAddress,
		UserAgent:  l.UserAgent,
		CreatedAt:  l.CreatedAt,
	}
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
		Limit:  conv.Int32(perPage),
		Offset: offset,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list audit logs")
		return
	}

	data := make([]AuditLogResponse, len(logs))
	for i, l := range logs {
		data[i] = auditLogResponse(l)
	}
	respond.List(w, data)
}
