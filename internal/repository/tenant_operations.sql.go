// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import (
	"context"
	"encoding/json"
	"time"
)

const tenantOperationColumns = `id, org_id, cluster_id, tenant_name, operation, status, git_commit_sha, error, values_json, template_id, created_by, created_at, completed_at`

func scanTenantOperation(row interface{ Scan(...interface{}) error }) (TenantOperation, error) {
	var op TenantOperation
	err := row.Scan(&op.ID, &op.OrgID, &op.ClusterID, &op.TenantName, &op.Operation, &op.Status, &op.GitCommitSHA, &op.Error, &op.ValuesJSON, &op.TemplateID, &op.CreatedBy, &op.CreatedAt, &op.CompletedAt)
	return op, err
}

type GetTenantOperationParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetTenantOperation(ctx context.Context, arg GetTenantOperationParams) (TenantOperation, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+tenantOperationColumns+` FROM tenant_operations WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanTenantOperation(row)
}

type ListTenantOperationsByTenantParams struct {
	ClusterID  string `json:"cluster_id"`
	OrgID      string `json:"org_id"`
	TenantName string `json:"tenant_name"`
}

// ListTenantOperationsByTenant returns every operation portal has attempted
// against a given tenant, newest first. Used by the UI Operations panel.
func (q *Queries) ListTenantOperationsByTenant(ctx context.Context, arg ListTenantOperationsByTenantParams) ([]TenantOperation, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+tenantOperationColumns+` FROM tenant_operations
		WHERE cluster_id = $1 AND org_id = $2 AND tenant_name = $3
		ORDER BY created_at DESC`,
		arg.ClusterID, arg.OrgID, arg.TenantName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []TenantOperation
	for rows.Next() {
		op, err := scanTenantOperation(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if ops == nil {
		ops = []TenantOperation{}
	}
	return ops, rows.Err()
}

// ListTenantOperationsByOrg returns the most recent tenant operations across
// every cluster in an org, most-recent-activity first — the tenant half of the
// org-wide ops feed. Ordered by COALESCE(completed_at, created_at) so the LIMIT
// trims by the same key the feed re-sorts on (see ListClusterOperationsByOrg).
func (q *Queries) ListTenantOperationsByOrg(ctx context.Context, orgID string) ([]TenantOperation, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+tenantOperationColumns+` FROM tenant_operations
		WHERE org_id = $1
		ORDER BY COALESCE(completed_at, created_at) DESC
		LIMIT 50`,
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []TenantOperation{}
	for rows.Next() {
		op, err := scanTenantOperation(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

type CreateTenantOperationParams struct {
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id"`
	ClusterID  string          `json:"cluster_id"`
	TenantName string          `json:"tenant_name"`
	Operation  string          `json:"operation"`
	ValuesJSON json.RawMessage `json:"values_json"`
	TemplateID *string         `json:"template_id"`
	CreatedBy  string          `json:"created_by"`
}

func (q *Queries) CreateTenantOperation(ctx context.Context, arg CreateTenantOperationParams) (TenantOperation, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO tenant_operations (id, org_id, cluster_id, tenant_name, operation, values_json, template_id, created_by)
		VALUES ($1, $2, $3, $4, $5::tenant_op_kind, $6, $7, $8)
		RETURNING `+tenantOperationColumns,
		arg.ID, arg.OrgID, arg.ClusterID, arg.TenantName, arg.Operation, arg.ValuesJSON, arg.TemplateID, arg.CreatedBy,
	)
	return scanTenantOperation(row)
}

type CompleteTenantOperationParams struct {
	ID           string    `json:"id"`
	OrgID        string    `json:"org_id"`
	Status       string    `json:"status"`
	GitCommitSHA string    `json:"git_commit_sha"`
	Error        string    `json:"error"`
	CompletedAt  time.Time `json:"completed_at"`
}

// CompleteTenantOperation transitions a pending operation row to its
// terminal status. On success: status='committed', git_commit_sha populated,
// error cleared. On failure: status='failed', error populated, sha empty.
func (q *Queries) CompleteTenantOperation(ctx context.Context, arg CompleteTenantOperationParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE tenant_operations
		SET status = $3::tenant_op_status,
		    git_commit_sha = $4,
		    error = $5,
		    completed_at = $6
		WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID, arg.Status, arg.GitCommitSHA, arg.Error, arg.CompletedAt,
	)
	return err
}
