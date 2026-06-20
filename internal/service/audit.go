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

// Log writes an audit row best-effort: a DB failure is logged but does not fail
// the caller. Use it for mutations whose audit trail is desirable but not
// legally load-bearing (create/update/delete of workspaces, clusters, etc.),
// where the mutation has already committed by the time we log.
//
// For compliance-relevant decisions that must not stand without their audit
// record — approve/reject of a gated apply, credential operations — do NOT use
// this. Use LogTx inside the mutation's transaction so the decision and its
// audit row commit or roll back together.
func (s *AuditService) Log(ctx context.Context, entry AuditEntry) {
	if err := s.logWith(ctx, s.queries, entry); err != nil {
		slog.Error("failed to write audit log", "error", err, "action", entry.Action, "entity_type", entry.EntityType, "entity_id", entry.EntityID)
	}
}

// LogTx writes an audit row using a transaction-bound queries handle (from
// Queries.WithTx) and returns the error so the caller can abort the surrounding
// transaction. The audit row then commits or rolls back atomically with the
// mutation it records — the durability guarantee compliance-relevant decisions
// need.
func (s *AuditService) LogTx(ctx context.Context, q *repository.Queries, entry AuditEntry) error {
	return s.logWith(ctx, q, entry)
}

// logWith is the shared insert path. q may be the service's pooled queries
// (best-effort Log) or a tx-bound handle (transactional LogTx).
func (s *AuditService) logWith(ctx context.Context, q *repository.Queries, entry AuditEntry) error {
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

	_, err = q.CreateAuditLog(ctx, repository.CreateAuditLogParams{
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
	return err
}

func marshalOrNull(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(v)
}
