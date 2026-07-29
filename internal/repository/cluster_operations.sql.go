// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const clusterOperationColumns = `id, org_id, name, environment, team, operation, status, git_commit_sha, error, spec_json, cluster_id, created_by, created_by_name, created_by_email, created_at, completed_at, vend_phases`

func scanClusterOperation(row interface{ Scan(...interface{}) error }) (ClusterOperation, error) {
	var op ClusterOperation
	err := row.Scan(&op.ID, &op.OrgID, &op.Name, &op.Environment, &op.Team, &op.Operation, &op.Status, &op.GitCommitSHA, &op.Error, &op.SpecJSON, &op.ClusterID, &op.CreatedBy, &op.CreatedByName, &op.CreatedByEmail, &op.CreatedAt, &op.CompletedAt, &op.VendPhases)
	return op, err
}

type GetClusterOperationParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetClusterOperation(ctx context.Context, arg GetClusterOperationParams) (ClusterOperation, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+clusterOperationColumns+` FROM cluster_operations WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanClusterOperation(row)
}

type ListClusterOperationsParams struct {
	OrgID       string `json:"org_id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
}

// ListClusterOperations returns every operation portal has attempted against a
// given cluster (by name+environment), newest first. Used by the UI Operations
// panel.
func (q *Queries) ListClusterOperations(ctx context.Context, arg ListClusterOperationsParams) ([]ClusterOperation, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+clusterOperationColumns+` FROM cluster_operations
		WHERE org_id = $1 AND name = $2 AND environment = $3
		ORDER BY created_at DESC`,
		arg.OrgID, arg.Name, arg.Environment,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []ClusterOperation{}
	for rows.Next() {
		op, err := scanClusterOperation(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// ListClusterOperationsByOrg returns the most recent operations across every
// cluster in an org, most-recent-activity first — powers the Clusters-tab order
// feed and the org-wide ops feed. Ordered by COALESCE(completed_at, created_at)
// so a just-finished vend floats up and the LIMIT trims by the SAME key the ops
// feed re-sorts on (otherwise a long-ago order that just completed could be
// dropped here before the merge ever sees it).
func (q *Queries) ListClusterOperationsByOrg(ctx context.Context, orgID string) ([]ClusterOperation, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+clusterOperationColumns+` FROM cluster_operations
		WHERE org_id = $1
		ORDER BY COALESCE(completed_at, created_at) DESC
		LIMIT 50`,
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []ClusterOperation{}
	for rows.Next() {
		op, err := scanClusterOperation(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// ProvisionInFlight reports whether a provision for this cluster name is still
// running anywhere in the deployment, and which environment it is vending into.
//
// 'pending' and 'committed' are the non-terminal provision states: the CR is
// either not yet written or written and building, and in neither case does a
// clusters row exist to collide with. That window is the whole point — it is
// where a second order for the same name would overwrite the first's manifest
// in the clusters repo while ArgoCD is mid-build. Not org-scoped, for the same
// reason ClusterNameTaken isn't: the manifest path it protects isn't either.
func (q *Queries) ProvisionInFlight(ctx context.Context, name string) (bool, string, error) {
	var environment string
	err := q.db.QueryRow(ctx,
		`SELECT environment FROM cluster_operations
		WHERE name = $1 AND operation = 'provision'::cluster_op_kind
		  AND status IN ('pending'::cluster_op_status, 'committed'::cluster_op_status)
		ORDER BY created_at DESC LIMIT 1`,
		name,
	).Scan(&environment)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "", nil
		}
		return false, "", err
	}
	return true, environment, nil
}

// ListClusterOperationsByStatus returns operations in a given status across all
// orgs, newest first. The provision watch-back uses it (status='committed') to
// find vended clusters whose status to poll; the worker is global like the tenant
// watcher, so this is intentionally not org-scoped.
func (q *Queries) ListClusterOperationsByStatus(ctx context.Context, status string) ([]ClusterOperation, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+clusterOperationColumns+` FROM cluster_operations
		WHERE status = $1::cluster_op_status
		ORDER BY created_at DESC`,
		status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ops := []ClusterOperation{}
	for rows.Next() {
		op, err := scanClusterOperation(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

type CreateClusterOperationParams struct {
	ID          string          `json:"id"`
	OrgID       string          `json:"org_id"`
	Name        string          `json:"name"`
	Environment string          `json:"environment"`
	Team        string          `json:"team"`
	Operation   string          `json:"operation"`
	SpecJSON    json.RawMessage `json:"spec_json"`
	CreatedBy   string          `json:"created_by"`
	// Resolved by the caller at enqueue. Nil when the lookup failed — the
	// operation still goes through; the commit just carries no trailer.
	CreatedByName  *string `json:"created_by_name"`
	CreatedByEmail *string `json:"created_by_email"`
}

func (q *Queries) CreateClusterOperation(ctx context.Context, arg CreateClusterOperationParams) (ClusterOperation, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO cluster_operations (id, org_id, name, environment, team, operation, spec_json, created_by, created_by_name, created_by_email)
		VALUES ($1, $2, $3, $4, $5, $6::cluster_op_kind, $7, $8, $9, $10)
		RETURNING `+clusterOperationColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Environment, arg.Team, arg.Operation, arg.SpecJSON, arg.CreatedBy,
		arg.CreatedByName, arg.CreatedByEmail,
	)
	return scanClusterOperation(row)
}

type CompleteClusterOperationParams struct {
	ID           string    `json:"id"`
	OrgID        string    `json:"org_id"`
	Status       string    `json:"status"`
	GitCommitSHA string    `json:"git_commit_sha"`
	Error        string    `json:"error"`
	CompletedAt  time.Time `json:"completed_at"`
}

// CompleteClusterOperation transitions a pending operation to its terminal
// commit status. On success: status='committed', git_commit_sha populated. On
// failure: status='failed', error populated. The 'active' transition (after the
// watch-back auto-registers the vended cluster) is a separate query.
func (q *Queries) CompleteClusterOperation(ctx context.Context, arg CompleteClusterOperationParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE cluster_operations
		SET status = $3::cluster_op_status,
		    git_commit_sha = $4,
		    error = $5,
		    completed_at = $6
		WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID, arg.Status, arg.GitCommitSHA, arg.Error, arg.CompletedAt,
	)
	return err
}

type ActivateClusterOperationParams struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	ClusterID   string    `json:"cluster_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// ActivateClusterOperation is the watch-back's terminal transition for a
// provision op: the vended cluster came up and was auto-registered, so the op
// goes to 'active' linked to the new clusters row. Idempotent on (id, org_id).
func (q *Queries) ActivateClusterOperation(ctx context.Context, arg ActivateClusterOperationParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE cluster_operations
		SET status = 'active'::cluster_op_status,
		    cluster_id = $3,
		    completed_at = $4
		WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID, arg.ClusterID, arg.CompletedAt,
	)
	return err
}

type DeprovisionClusterOperationParams struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// DeprovisionClusterOperation is the watch-back's terminal transition for a
// deprovision op: the Cluster XR is gone from the hub, so Crossplane finished
// tearing the cluster down. Guarded WHERE status='committed' so it's idempotent
// across ticks and can't double-close or resurrect a row.
func (q *Queries) DeprovisionClusterOperation(ctx context.Context, arg DeprovisionClusterOperationParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE cluster_operations
		SET status = 'deprovisioned'::cluster_op_status,
		    completed_at = $3
		WHERE id = $1 AND org_id = $2 AND status = 'committed'`,
		arg.ID, arg.OrgID, arg.CompletedAt,
	)
	return err
}

type ExpireClusterOperationParams struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Error       string    `json:"error"`
	CompletedAt time.Time `json:"completed_at"`
}

// ExpireClusterOperation is the watch-back's terminal transition for a committed
// provision whose Cluster XR never appeared on the hub (ArgoCD not syncing, the
// order abandoned, or the CR deleted out-of-band). It marks the op failed with a
// reason so the row leaves the committed working set instead of being
// re-reconciled forever. Guarded WHERE status='committed' so it can't race the
// active flip or double-close.
func (q *Queries) ExpireClusterOperation(ctx context.Context, arg ExpireClusterOperationParams) error {
	_, err := q.db.Exec(ctx,
		`UPDATE cluster_operations
		SET status = 'failed'::cluster_op_status,
		    error = $3,
		    completed_at = $4
		WHERE id = $1 AND org_id = $2 AND status = 'committed'`,
		arg.ID, arg.OrgID, arg.Error, arg.CompletedAt,
	)
	return err
}

// SetVendPhase merges a single phase checkpoint into vend_phases (a regressible
// map keyed by phase). jsonb `||` overwrites the key, so a phase can advance or
// move backward as the substrate's truth changes. `phase` is a one-key object,
// e.g. {"committed": {"at": "...", "detail": ""}}.
func (q *Queries) SetVendPhase(ctx context.Context, id, orgID string, phase json.RawMessage) error {
	_, err := q.db.Exec(ctx,
		`UPDATE cluster_operations SET vend_phases = vend_phases || $3::jsonb WHERE id = $1 AND org_id = $2`,
		id, orgID, phase,
	)
	return err
}
