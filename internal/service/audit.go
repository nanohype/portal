package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/repository"
)

type AuditEntry struct {
	OrgID      string
	UserID     string
	Action     string
	EntityType string
	EntityID   string
	Before     any
	After      any
	IPAddress  string
	UserAgent  string
}

type AuditService struct {
	queries *repository.Queries
}

func NewAuditService(queries *repository.Queries) *AuditService {
	return &AuditService{queries: queries}
}

func (s *AuditService) Log(ctx context.Context, entry AuditEntry) {
	beforeData, err := marshalOrNull(entry.Before)
	if err != nil {
		slog.Warn("failed to marshal audit before_data", "error", err)
		beforeData = json.RawMessage("null")
	}

	afterData, err := marshalOrNull(entry.After)
	if err != nil {
		slog.Warn("failed to marshal audit after_data", "error", err)
		afterData = json.RawMessage("null")
	}

	_, err = s.queries.CreateAuditLog(ctx, repository.CreateAuditLogParams{
		ID:         ulid.Make().String(),
		OrgID:      entry.OrgID,
		UserID:     entry.UserID,
		Action:     entry.Action,
		EntityType: entry.EntityType,
		EntityID:   entry.EntityID,
		BeforeData: beforeData,
		AfterData:  afterData,
		IPAddress:  entry.IPAddress,
		UserAgent:  entry.UserAgent,
	})
	if err != nil {
		slog.Error("failed to write audit log", "error", err, "action", entry.Action, "entity_type", entry.EntityType, "entity_id", entry.EntityID)
	}
}

func marshalOrNull(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(v)
}
