package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/stxkxs/tofui/internal/repository"
)

type TenantService struct {
	queries *repository.Queries
	db      *pgxpool.Pool
}

func NewTenantService(queries *repository.Queries, db *pgxpool.Pool) *TenantService {
	return &TenantService{queries: queries, db: db}
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

func (s *TenantService) List(ctx context.Context, orgID, clusterID string, page, perPage int) ([]repository.Tenant, int64, error) {
	offset := int32((page - 1) * perPage)

	tenants, err := s.queries.ListTenants(ctx, repository.ListTenantsParams{
		OrgID:     orgID,
		ClusterID: clusterID,
		Limit:     int32(perPage),
		Offset:    offset,
	})
	if err != nil {
		return nil, 0, err
	}

	count, err := s.queries.CountTenants(ctx, repository.CountTenantsParams{
		OrgID: orgID, ClusterID: clusterID,
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
// of truth; tofui's row state converges to it on each watch tick.
//
// Returns the number of upserts + deletes performed so the watcher can log
// useful telemetry.
func (s *TenantService) Reconcile(ctx context.Context, orgID, clusterID string, observed []TenantSnapshot) (upserts int, deletes int, err error) {
	now := time.Now()
	observedNames := make(map[string]struct{}, len(observed))

	for _, t := range observed {
		observedNames[t.Name] = struct{}{}
		_, err := s.queries.UpsertTenant(ctx, repository.UpsertTenantParams{
			ID:             ulid.Make().String(),
			OrgID:          orgID,
			ClusterID:      clusterID,
			Name:           t.Name,
			Phase:          t.Phase,
			Spec:           nonNullJSON(t.Spec),
			Status:         nonNullJSON(t.Status),
			LastObservedAt: now,
		})
		if err != nil {
			return upserts, deletes, fmt.Errorf("upsert tenant %s: %w", t.Name, err)
		}
		upserts++
	}

	existing, err := s.queries.ListTenantNamesByCluster(ctx, clusterID, orgID)
	if err != nil {
		return upserts, deletes, fmt.Errorf("list existing tenant names: %w", err)
	}
	var toDelete []string
	for _, name := range existing {
		if _, ok := observedNames[name]; !ok {
			toDelete = append(toDelete, name)
		}
	}
	if len(toDelete) > 0 {
		if err := s.queries.DeleteTenantsByClusterAndNames(ctx, repository.DeleteTenantsByClusterAndNamesParams{
			ClusterID: clusterID, OrgID: orgID, Names: toDelete,
		}); err != nil {
			return upserts, deletes, fmt.Errorf("delete stale tenants: %w", err)
		}
		deletes = len(toDelete)
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
