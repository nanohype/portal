// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import (
	"context"
	"encoding/json"
)

const auditLogColumns = `id, org_id, user_id, action, entity_type, entity_id, before_data, after_data, ip_address, user_agent, created_at`

func scanAuditLog(row interface{ Scan(...interface{}) error }) (AuditLog, error) {
	var a AuditLog
	err := row.Scan(&a.ID, &a.OrgID, &a.UserID, &a.Action, &a.EntityType, &a.EntityID, &a.BeforeData, &a.AfterData, &a.IPAddress, &a.UserAgent, &a.CreatedAt)
	return a, err
}

type CreateAuditLogParams struct {
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
}

func (q *Queries) CreateAuditLog(ctx context.Context, arg CreateAuditLogParams) (AuditLog, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO audit_logs (id, org_id, user_id, action, entity_type, entity_id, before_data, after_data, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+auditLogColumns,
		arg.ID, arg.OrgID, arg.UserID, arg.Action, arg.EntityType, arg.EntityID, arg.BeforeData, arg.AfterData, arg.IPAddress, arg.UserAgent,
	)
	return scanAuditLog(row)
}

type ListAuditLogsParams struct {
	OrgID  string `json:"org_id"`
	Limit  int32  `json:"limit"`
	Offset int32  `json:"offset"`
}

func (q *Queries) ListAuditLogs(ctx context.Context, arg ListAuditLogsParams) ([]AuditLog, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+auditLogColumns+` FROM audit_logs WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		arg.OrgID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		a, err := scanAuditLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, a)
	}
	if logs == nil {
		logs = []AuditLog{}
	}
	return logs, rows.Err()
}
