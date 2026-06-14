// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import (
	"context"
	"time"
)

const clusterColumns = `id, org_id, account_id, name, description, environment, api_endpoint, ca_bundle_encrypted, sa_token_encrypted, region, auth_mode, eks_cluster_name, connection_status, last_connected_at, connection_error, node_count, k8s_version, argocd_sync_status, argocd_health_status, control_plane_status, platform_version, last_health_observed_at, created_by, created_at, updated_at`

func scanCluster(row interface{ Scan(...interface{}) error }) (Cluster, error) {
	var c Cluster
	err := row.Scan(&c.ID, &c.OrgID, &c.AccountID, &c.Name, &c.Description, &c.Environment, &c.APIEndpoint, &c.CABundleEncrypted, &c.SATokenEncrypted, &c.Region, &c.AuthMode, &c.EKSClusterName, &c.ConnectionStatus, &c.LastConnectedAt, &c.ConnectionError, &c.NodeCount, &c.K8sVersion, &c.ArgoCDSyncStatus, &c.ArgoCDHealthStatus, &c.ControlPlaneStatus, &c.PlatformVersion, &c.LastHealthObservedAt, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

type GetClusterParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetCluster(ctx context.Context, arg GetClusterParams) (Cluster, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+clusterColumns+` FROM clusters WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanCluster(row)
}

type GetClusterByNameParams struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

// GetClusterByName looks up a cluster by its org-unique name. The provision
// watch-back uses it to stay idempotent: if a prior tick already registered
// the vended cluster but crashed before activating the op, the next tick finds
// the existing row instead of colliding on the UNIQUE(org_id, name) constraint.
func (q *Queries) GetClusterByName(ctx context.Context, arg GetClusterByNameParams) (Cluster, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+clusterColumns+` FROM clusters WHERE org_id = $1 AND name = $2`,
		arg.OrgID, arg.Name,
	)
	return scanCluster(row)
}

type ListClustersParams struct {
	OrgID     string `json:"org_id"`
	AccountID string `json:"account_id"`
	Limit     int32  `json:"limit"`
	Offset    int32  `json:"offset"`
}

func (q *Queries) ListClusters(ctx context.Context, arg ListClustersParams) ([]Cluster, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+clusterColumns+` FROM clusters
		WHERE org_id = $1
		  AND ($2::TEXT = '' OR account_id = $2)
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		arg.OrgID, arg.AccountID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clusters []Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	if clusters == nil {
		clusters = []Cluster{}
	}
	return clusters, rows.Err()
}

type CountClustersParams struct {
	OrgID     string `json:"org_id"`
	AccountID string `json:"account_id"`
}

func (q *Queries) CountClusters(ctx context.Context, arg CountClustersParams) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM clusters
		WHERE org_id = $1
		  AND ($2::TEXT = '' OR account_id = $2)`,
		arg.OrgID, arg.AccountID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

type CreateClusterParams struct {
	ID                string `json:"id"`
	OrgID             string `json:"org_id"`
	AccountID         string `json:"account_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Environment       string `json:"environment"`
	APIEndpoint       string `json:"api_endpoint"`
	CABundleEncrypted string `json:"ca_bundle_encrypted"`
	SATokenEncrypted  string `json:"sa_token_encrypted"`
	Region            string `json:"region"`
	AuthMode          string `json:"auth_mode"`
	EKSClusterName    string `json:"eks_cluster_name"`
	CreatedBy         string `json:"created_by"`
}

func (q *Queries) CreateCluster(ctx context.Context, arg CreateClusterParams) (Cluster, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO clusters (id, org_id, account_id, name, description, environment, api_endpoint, ca_bundle_encrypted, sa_token_encrypted, region, auth_mode, eks_cluster_name, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+clusterColumns,
		arg.ID, arg.OrgID, arg.AccountID, arg.Name, arg.Description, arg.Environment, arg.APIEndpoint, arg.CABundleEncrypted, arg.SATokenEncrypted, arg.Region, arg.AuthMode, arg.EKSClusterName, arg.CreatedBy,
	)
	return scanCluster(row)
}

type UpdateClusterParams struct {
	ID                string `json:"id"`
	OrgID             string `json:"org_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Environment       string `json:"environment"`
	APIEndpoint       string `json:"api_endpoint"`
	CABundleEncrypted string `json:"ca_bundle_encrypted"`
	SATokenEncrypted  string `json:"sa_token_encrypted"`
	Region            string `json:"region"`
}

func (q *Queries) UpdateCluster(ctx context.Context, arg UpdateClusterParams) (Cluster, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE clusters
		SET name = COALESCE(NULLIF($3, ''), name),
			description = COALESCE(NULLIF($4, ''), description),
			environment = COALESCE(NULLIF($5, ''), environment),
			api_endpoint = COALESCE(NULLIF($6, ''), api_endpoint),
			ca_bundle_encrypted = COALESCE(NULLIF($7, ''), ca_bundle_encrypted),
			sa_token_encrypted = COALESCE(NULLIF($8, ''), sa_token_encrypted),
			region = COALESCE(NULLIF($9, ''), region),
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+clusterColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.Environment, arg.APIEndpoint, arg.CABundleEncrypted, arg.SATokenEncrypted, arg.Region,
	)
	return scanCluster(row)
}

type DeleteClusterParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) DeleteCluster(ctx context.Context, arg DeleteClusterParams) error {
	_, err := q.db.Exec(ctx,
		`DELETE FROM clusters WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return err
}

// ClusterWatchTarget is the minimal pair the dispatch tick needs to enqueue
// a per-cluster watch job — no need to load the full cluster row at the
// dispatch site.
type ClusterWatchTarget struct {
	ID    string
	OrgID string
}

// ListConnectedClusters returns just the IDs of clusters with
// connection_status='connected' across all orgs. Used by the watcher's
// dispatch tick.
func (q *Queries) ListConnectedClusters(ctx context.Context) ([]ClusterWatchTarget, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id FROM clusters WHERE connection_status = 'connected'`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []ClusterWatchTarget
	for rows.Next() {
		var t ClusterWatchTarget
		if err := rows.Scan(&t.ID, &t.OrgID); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// SetClusterConnectionStatus writes the async connection-test job's results.
// On success: status='connected', last_connected_at=now(), node_count + k8s_version
// populated, error cleared. On failure: status='failed', error populated,
// other fields untouched so the last good values stay visible.
type SetClusterConnectionStatusParams struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	NodeCount  int32     `json:"node_count"`
	K8sVersion string    `json:"k8s_version"`
	At         time.Time `json:"at"`
}

func (q *Queries) SetClusterConnectionStatus(ctx context.Context, arg SetClusterConnectionStatusParams) error {
	if arg.Status == "connected" {
		_, err := q.db.Exec(ctx,
			`UPDATE clusters
			SET connection_status = $3::cluster_connection_status,
				last_connected_at = $4,
				connection_error = '',
				node_count = $5,
				k8s_version = $6,
				updated_at = NOW()
			WHERE id = $1 AND org_id = $2`,
			arg.ID, arg.OrgID, arg.Status, arg.At, arg.NodeCount, arg.K8sVersion,
		)
		return err
	}
	_, err := q.db.Exec(ctx,
		`UPDATE clusters
		SET connection_status = $3::cluster_connection_status,
			connection_error = $4,
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID, arg.Status, arg.Error,
	)
	return err
}

// ClusterHealthTarget is the slim row the hub-side health watcher iterates — the
// fields it needs to locate a cluster's per-cluster ArgoCD Application
// (environment+name) and to describe its EKS control plane (region+eks_cluster_name
// + the account whose role grants eks:DescribeCluster). No credentials: the health
// read talks to the hub + AWS, never the spoke.
type ClusterHealthTarget struct {
	ID             string `json:"id"`
	OrgID          string `json:"org_id"`
	AccountID      string `json:"account_id"`
	Name           string `json:"name"`
	Environment    string `json:"environment"`
	Region         string `json:"region"`
	AuthMode       string `json:"auth_mode"`
	EKSClusterName string `json:"eks_cluster_name"`
}

// ListClusterHealthTargets returns every registered cluster across all orgs — the
// health watcher is global like the other in-cluster watchers, and health is
// independent of connection status (the ArgoCD Application lives on the hub, not
// the spoke, so a cluster portal can't reach can still report ArgoCD health).
func (q *Queries) ListClusterHealthTargets(ctx context.Context) ([]ClusterHealthTarget, error) {
	// Cap kept in sync with clusterHealthTargetCap so the watcher can warn when
	// it's hit rather than silently dropping clusters from the sweep.
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id, account_id, name, environment, region, auth_mode, eks_cluster_name
		FROM clusters
		ORDER BY created_at DESC
		LIMIT 10000`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []ClusterHealthTarget{}
	for rows.Next() {
		var t ClusterHealthTarget
		if err := rows.Scan(&t.ID, &t.OrgID, &t.AccountID, &t.Name, &t.Environment, &t.Region, &t.AuthMode, &t.EKSClusterName); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// SetClusterArgoCDHealth writes the per-cluster ArgoCD Application's sync + health.
// Called only when the Application read produced a definitive result (NotFound ->
// empty, "this cluster has no per-cluster Application"); a transient read error
// skips the call so the last-known values are preserved.
func (q *Queries) SetClusterArgoCDHealth(ctx context.Context, id, orgID, sync, health string, at time.Time) error {
	_, err := q.db.Exec(ctx,
		`UPDATE clusters
		SET argocd_sync_status = $3,
			argocd_health_status = $4,
			last_health_observed_at = $5,
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2`,
		id, orgID, sync, health, at,
	)
	return err
}

// SetClusterControlPlane writes the EKS control-plane status + platform version
// from eks:DescribeCluster. Called only on a successful describe; an error
// (AccessDenied, throttle, non-EKS) skips the call so prior values are preserved.
func (q *Queries) SetClusterControlPlane(ctx context.Context, id, orgID, status, platformVersion string, at time.Time) error {
	_, err := q.db.Exec(ctx,
		`UPDATE clusters
		SET control_plane_status = $3,
			platform_version = $4,
			last_health_observed_at = $5,
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2`,
		id, orgID, status, platformVersion, at,
	)
	return err
}
