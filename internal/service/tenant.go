package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/clusterspec"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker"
)

type TenantService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	riverClient *river.Client[pgx.Tx]
}

func NewTenantService(queries *repository.Queries, db *pgxpool.Pool) *TenantService {
	return &TenantService{queries: queries, db: db}
}

func (s *TenantService) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.riverClient = client
}

// TenantSnapshot is the watcher's view of one Tenant CR. Wire format for
// crossing the worker→service boundary without leaking k8s types into the
// service package.
type TenantSnapshot struct {
	Name   string
	Phase  string
	Spec   json.RawMessage
	Status json.RawMessage
}

// List returns tenants visible to the caller. teamIDs=nil for admin (no
// scoping); non-nil slice (possibly empty) for non-admin — empty means
// "user belongs to no teams" → zero rows.
func (s *TenantService) List(ctx context.Context, orgID, clusterID string, teamIDs []string, page, perPage int) ([]repository.Tenant, int64, error) {
	offset := int32((page - 1) * perPage)

	tenants, err := s.queries.ListTenants(ctx, repository.ListTenantsParams{
		OrgID:     orgID,
		ClusterID: clusterID,
		TeamIDs:   teamIDs,
		Limit:     int32(perPage),
		Offset:    offset,
	})
	if err != nil {
		return nil, 0, err
	}

	count, err := s.queries.CountTenants(ctx, repository.CountTenantsParams{
		OrgID: orgID, ClusterID: clusterID, TeamIDs: teamIDs,
	})
	if err != nil {
		return nil, 0, err
	}

	return tenants, count, nil
}

func (s *TenantService) Get(ctx context.Context, id, orgID string) (repository.Tenant, error) {
	return s.queries.GetTenant(ctx, repository.GetTenantParams{ID: id, OrgID: orgID})
}

// Reconcile is the load-bearing watcher method. Given the freshly-observed
// set of tenants in a cluster, it upserts every observed row and deletes any
// DB row whose name no longer appears in the observed set. K8s is the source
// of truth; portal's row state converges to it on each watch tick.
//
// Returns the number of upserts + deletes performed so the watcher can log
// useful telemetry.
func (s *TenantService) Reconcile(ctx context.Context, orgID, clusterID string, observed []TenantSnapshot) (upserts int, deletes int, err error) {
	now := time.Now()
	observedNames := make(map[string]struct{}, len(observed))
	ids := make([]string, len(observed))
	names := make([]string, len(observed))
	phases := make([]string, len(observed))
	specs := make([]string, len(observed))
	statuses := make([]string, len(observed))
	for i, t := range observed {
		observedNames[t.Name] = struct{}{}
		ids[i] = ulid.Make().String()
		names[i] = t.Name
		phases[i] = t.Phase
		specs[i] = string(nonNullJSON(t.Spec))
		statuses[i] = string(nonNullJSON(t.Status))
	}

	// Upsert (one batched statement) + prune-stale in one transaction so a tick
	// is atomic — the inventory never reflects a half-applied snapshot.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin reconcile tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	if len(observed) > 0 {
		if err := qtx.BatchUpsertTenants(ctx, repository.BatchUpsertTenantsParams{
			OrgID: orgID, ClusterID: clusterID, LastObservedAt: now,
			IDs: ids, Names: names, Phases: phases, Specs: specs, Statuses: statuses,
		}); err != nil {
			return 0, 0, fmt.Errorf("batch upsert tenants: %w", err)
		}
		upserts = len(observed)
	}

	existing, err := qtx.ListTenantNamesByCluster(ctx, clusterID, orgID)
	if err != nil {
		return upserts, 0, fmt.Errorf("list existing tenant names: %w", err)
	}
	var toDelete []string
	for _, name := range existing {
		if _, ok := observedNames[name]; !ok {
			toDelete = append(toDelete, name)
		}
	}
	if len(toDelete) > 0 {
		if err := qtx.DeleteTenantsByClusterAndNames(ctx, repository.DeleteTenantsByClusterAndNamesParams{
			ClusterID: clusterID, OrgID: orgID, Names: toDelete,
		}); err != nil {
			return upserts, 0, fmt.Errorf("delete stale tenants: %w", err)
		}
		deletes = len(toDelete)
	}

	if err := tx.Commit(ctx); err != nil {
		return upserts, deletes, fmt.Errorf("commit reconcile tx: %w", err)
	}
	return upserts, deletes, nil
}

// nonNullJSON guarantees a column-valid value for JSONB columns: an empty
// `{}` when the watcher hands us nil so the NOT NULL DEFAULT '{}' stays
// honored at INSERT time and isn't blanked by an explicit NULL.
func nonNullJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}

// CreateTenantInput is the validated shape of a tenant-create request plus the
// caller context needed to authorize it. Validate() + EnqueueCreate own the
// tenant identity rules so no handler can skip them.
type CreateTenantInput struct {
	OrgID        string
	ClusterID    string
	Name         string
	TemplateID   string // optional; recorded on the operation row
	OwningTeamID string
	CreatedBy    string
	Values       map[string]interface{}
}

// Normalize trims the identity fields and defaults the values blob so an empty
// body still renders. Call before Validate / template-apply.
func (in *CreateTenantInput) Normalize() {
	in.ClusterID = strings.TrimSpace(in.ClusterID)
	in.Name = strings.TrimSpace(in.Name)
	in.OwningTeamID = strings.TrimSpace(in.OwningTeamID)
	if in.Values == nil {
		in.Values = map[string]interface{}{}
	}
}

// Validate enforces the tenant identity rules: a cluster is required and the
// name must be a valid RFC-1123 label — it becomes a k8s resource name and a
// filename in the tenants repo. Returns apperr.Validation so the handler maps
// it to 400 via respond.FromError.
func (in CreateTenantInput) Validate() error {
	if in.ClusterID == "" {
		return apperr.Validation("cluster_id is required")
	}
	if !clusterspec.ValidName(in.Name) {
		return apperr.Validation("name must be a valid Kubernetes name (lowercase alphanumeric + hyphen, 1-63 chars)")
	}
	return nil
}

// ResolveOwningTeam picks the team that will own a new tenant and authorizes the
// caller's choice. An admin may omit it (no ownership = admin-only visibility).
// A non-admin defaults to their sole team, must name one when in several, and
// can only name a team they belong to. Bad-input cases return apperr.Validation.
func ResolveOwningTeam(isAdmin bool, owningTeamID string, userTeams []string) (string, error) {
	if isAdmin {
		return owningTeamID, nil
	}
	switch {
	case len(userTeams) == 0:
		return "", apperr.Validation("you must belong to a team before creating tenants")
	case owningTeamID == "" && len(userTeams) == 1:
		return userTeams[0], nil
	case owningTeamID == "":
		return "", apperr.Validation("owning_team_id is required when you belong to multiple teams")
	default:
		for _, t := range userTeams {
			if t == owningTeamID {
				return owningTeamID, nil
			}
		}
		return "", apperr.Validation("owning_team_id must be a team you belong to")
	}
}

// forcePlatformIdentity stamps the authoritative tenant identity onto the values
// blob: platform.name is always the validated tenant name (the operator derives
// the namespace / IRSA boundary / git path from it, so it must never diverge),
// and platform.tenant defaults to the same name when unset. A caller-supplied
// blob can't point the rendered Platform at another tenant's name.
func forcePlatformIdentity(values map[string]interface{}, name string) {
	pf, _ := values["platform"].(map[string]interface{})
	if pf == nil {
		pf = map[string]interface{}{}
		values["platform"] = pf
	}
	pf["name"] = name
	if t, _ := pf["tenant"].(string); t == "" {
		pf["tenant"] = name
	}
}

// EnqueueCreate validates the input, forces the authoritative Platform identity
// onto the values blob, records a "portal wants this tenant to exist" intent,
// and schedules the worker job that renders the chart + commits to git. The
// returned TenantOperation carries status=pending until the worker transitions
// it. Idempotency lives at the worker — repeated create commits with identical
// content are no-ops because git status stays clean.
//
// Validation runs before the DB write so a bad form never creates a dangling
// operation row (mirrors ClusterOrderService.EnqueueProvision). The caller
// ApplyToValues-es any referenced template before this layer; the service
// trusts the merged values but always re-forces platform.name/platform.tenant.
func (s *TenantService) EnqueueCreate(ctx context.Context, in CreateTenantInput) (repository.TenantOperation, error) {
	if err := in.Validate(); err != nil {
		return repository.TenantOperation{}, err
	}
	values := in.Values
	if values == nil {
		values = map[string]interface{}{}
	}
	forcePlatformIdentity(values, in.Name)
	return s.enqueue(ctx, in.OrgID, in.ClusterID, in.Name, "create", in.TemplateID, in.CreatedBy, values)
}

// EnqueueDelete is the symmetric operation: records intent to remove a
// tenant and schedules the worker job to delete its file from the tenants
// repo and commit.
func (s *TenantService) EnqueueDelete(ctx context.Context, orgID, clusterID, name, createdBy string) (repository.TenantOperation, error) {
	return s.enqueue(ctx, orgID, clusterID, name, "delete", "", createdBy, nil)
}

func (s *TenantService) enqueue(ctx context.Context, orgID, clusterID, name, kind, templateID, createdBy string, values map[string]interface{}) (repository.TenantOperation, error) {
	if s.riverClient == nil {
		return repository.TenantOperation{}, fmt.Errorf("river client not configured")
	}
	var raw json.RawMessage
	if values != nil {
		b, err := json.Marshal(values)
		if err != nil {
			return repository.TenantOperation{}, fmt.Errorf("marshal values: %w", err)
		}
		raw = b
	} else {
		raw = json.RawMessage("{}")
	}

	var tmplPtr *string
	if templateID != "" {
		tmplPtr = &templateID
	}
	op, err := s.queries.CreateTenantOperation(ctx, repository.CreateTenantOperationParams{
		ID:         ulid.Make().String(),
		OrgID:      orgID,
		ClusterID:  clusterID,
		TenantName: name,
		Operation:  kind,
		ValuesJSON: raw,
		TemplateID: tmplPtr,
		CreatedBy:  createdBy,
	})
	if err != nil {
		return repository.TenantOperation{}, fmt.Errorf("create operation: %w", err)
	}

	if _, err := s.riverClient.Insert(ctx, worker.TenantApplyJobArgs{
		OperationID: op.ID, OrgID: op.OrgID,
	}, nil); err != nil {
		// The operation row exists in pending; a future explicit retry can
		// recover. We still surface the error so the handler returns 500.
		return op, fmt.Errorf("enqueue job: %w", err)
	}
	return op, nil
}

// CompleteOperation is the write path the worker uses to mark an operation
// done. Wrapped so callers don't need to construct the params struct.
func (s *TenantService) CompleteOperation(ctx context.Context, id, orgID, status, sha, errMsg string) error {
	return s.queries.CompleteTenantOperation(ctx, repository.CompleteTenantOperationParams{
		ID:           id,
		OrgID:        orgID,
		Status:       status,
		GitCommitSHA: sha,
		Error:        errMsg,
		CompletedAt:  time.Now(),
	})
}

// GetOperation reads an operation row by ID. Used by the worker on job start.
func (s *TenantService) GetOperation(ctx context.Context, id, orgID string) (repository.TenantOperation, error) {
	return s.queries.GetTenantOperation(ctx, repository.GetTenantOperationParams{ID: id, OrgID: orgID})
}

// ListOperations returns the per-tenant operation log for the UI panel.
func (s *TenantService) ListOperations(ctx context.Context, orgID, clusterID, tenantName string) ([]repository.TenantOperation, error) {
	return s.queries.ListTenantOperationsByTenant(ctx, repository.ListTenantOperationsByTenantParams{
		ClusterID: clusterID, OrgID: orgID, TenantName: tenantName,
	})
}
