package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// ClusterOrderService is the vend order desk: it records provision/deprovision
// intents as cluster_operations rows and schedules the worker that commits the
// Cluster CR to the clusters GitOps repo. It mirrors TenantService's write path.
type ClusterOrderService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	riverClient *river.Client[pgx.Tx]
}

func NewClusterOrderService(queries *repository.Queries, db *pgxpool.Pool) *ClusterOrderService {
	return &ClusterOrderService{queries: queries, db: db}
}

func (s *ClusterOrderService) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.riverClient = client
}

// EnqueueProvision validates the spec, records the intent, and schedules the
// apply worker. Validation runs before the DB write so a bad form never creates
// a dangling operation row.
func (s *ClusterOrderService) EnqueueProvision(ctx context.Context, orgID, createdBy string, input clusterspec.Input) (repository.ClusterOperation, error) {
	if err := input.Validate(); err != nil {
		return repository.ClusterOperation{}, err
	}
	return s.enqueue(ctx, orgID, "provision", createdBy, input)
}

// EnqueueDeprovision records intent to tear a cluster down (remove its file →
// ArgoCD prunes → Crossplane tofu destroy). name+environment locate the manifest;
// team is recorded for the audit trail.
func (s *ClusterOrderService) EnqueueDeprovision(ctx context.Context, orgID, name, environment, team, createdBy string) (repository.ClusterOperation, error) {
	return s.enqueue(ctx, orgID, "deprovision", createdBy, clusterspec.Input{
		Name: name, Environment: environment, Team: team,
	})
}

// EnqueueUnwedge records intent to break-glass tear down a wedged spoke and
// schedules the unwedge worker. Unlike provision/deprovision (which write git),
// this drives a direct AWS teardown, so it needs the workload account + region —
// which live only on the original provision op's spec (a wedged spoke never
// finished provisioning, so it never became a registered cluster). We copy that
// spec onto the unwedge op so the worker has the target without a cluster record.
func (s *ClusterOrderService) EnqueueUnwedge(ctx context.Context, orgID, name, environment, team, createdBy string) (repository.ClusterOperation, error) {
	if s.riverClient == nil {
		return repository.ClusterOperation{}, fmt.Errorf("river client not configured")
	}

	spec, err := s.provisionSpec(ctx, orgID, name, environment)
	if err != nil {
		return repository.ClusterOperation{}, err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return repository.ClusterOperation{}, fmt.Errorf("marshal spec: %w", err)
	}

	op, err := s.queries.CreateClusterOperation(ctx, repository.CreateClusterOperationParams{
		ID:          ulid.Make().String(),
		OrgID:       orgID,
		Name:        name,
		Environment: spec.EffectiveEnvironment(),
		Team:        team,
		Operation:   "unwedge",
		SpecJSON:    raw,
		CreatedBy:   createdBy,
	})
	if err != nil {
		return repository.ClusterOperation{}, fmt.Errorf("create operation: %w", err)
	}
	if _, err := s.riverClient.Insert(ctx, worker.ClusterUnwedgeJobArgs{
		OperationID: op.ID, OrgID: op.OrgID,
	}, nil); err != nil {
		return op, fmt.Errorf("enqueue job: %w", err)
	}
	return op, nil
}

// provisionSpec recovers the most recent provision op's spec for a cluster — the
// source of the workload account + region the unwedge teardown assumes into.
func (s *ClusterOrderService) provisionSpec(ctx context.Context, orgID, name, environment string) (clusterspec.Input, error) {
	ops, err := s.queries.ListClusterOperations(ctx, repository.ListClusterOperationsParams{
		OrgID: orgID, Name: name, Environment: environment,
	})
	if err != nil {
		return clusterspec.Input{}, fmt.Errorf("list operations: %w", err)
	}
	return pickProvisionSpec(ops, environment, name)
}

// pickProvisionSpec finds the most recent provision op's spec in a newest-first
// operation list — the only record of a wedged spoke's workload account + region.
// Pure so the selection (skip non-provision, require account/region) is tested
// without a database.
func pickProvisionSpec(ops []repository.ClusterOperation, environment, name string) (clusterspec.Input, error) {
	for _, op := range ops {
		if op.Operation != "provision" {
			continue
		}
		var spec clusterspec.Input
		if err := json.Unmarshal(op.SpecJSON, &spec); err != nil {
			return clusterspec.Input{}, fmt.Errorf("unmarshal provision spec: %w", err)
		}
		if spec.Account == "" || spec.Region == "" {
			return clusterspec.Input{}, apperr.Conflict(fmt.Sprintf("provision op %s has no account/region on record", op.ID))
		}
		return spec, nil
	}
	return clusterspec.Input{}, apperr.Conflict(fmt.Sprintf("no provision on record for %s/%s; cannot determine the workload account to unwedge", environment, name))
}

func (s *ClusterOrderService) enqueue(ctx context.Context, orgID, kind, createdBy string, input clusterspec.Input) (repository.ClusterOperation, error) {
	if s.riverClient == nil {
		return repository.ClusterOperation{}, fmt.Errorf("river client not configured")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return repository.ClusterOperation{}, fmt.Errorf("marshal spec: %w", err)
	}
	op, err := s.queries.CreateClusterOperation(ctx, repository.CreateClusterOperationParams{
		ID:          ulid.Make().String(),
		OrgID:       orgID,
		Name:        input.Name,
		Environment: input.EffectiveEnvironment(),
		Team:        input.Team,
		Operation:   kind,
		SpecJSON:    raw,
		CreatedBy:   createdBy,
	})
	if err != nil {
		return repository.ClusterOperation{}, fmt.Errorf("create operation: %w", err)
	}
	if _, err := s.riverClient.Insert(ctx, worker.ClusterApplyJobArgs{
		OperationID: op.ID, OrgID: op.OrgID,
	}, nil); err != nil {
		return op, fmt.Errorf("enqueue job: %w", err)
	}
	return op, nil
}

// CompleteOperation is the write path the worker uses to mark an operation done.
func (s *ClusterOrderService) CompleteOperation(ctx context.Context, id, orgID, status, sha, errMsg string) error {
	if err := s.queries.CompleteClusterOperation(ctx, repository.CompleteClusterOperationParams{
		ID:           id,
		OrgID:        orgID,
		Status:       status,
		GitCommitSHA: sha,
		Error:        errMsg,
		CompletedAt:  time.Now(),
	}); err != nil {
		return err
	}
	// Project the portal-side terminal transition onto the vend timeline. status
	// is "committed" or "failed"; the substrate phases (tofu_running, active) are
	// written later by the in-cluster watcher. vend_phases is a best-effort
	// projection, not the verdict — the status row above is authoritative, so a
	// projection-write hiccup must not fail a job whose operation actually
	// completed. Log and move on.
	detail := ""
	if status == "failed" {
		detail = errMsg
	}
	if err := s.setVendPhase(ctx, id, orgID, status, detail); err != nil {
		slog.WarnContext(ctx, "vend phase projection failed", "op", id, "phase", status, "error", err)
	}
	return nil
}

// RecordPhase advances an operation's vend_phases timeline mid-run. The unwedge
// worker uses it to project teardown progress (verified → tearing-down →
// torn-down) onto the row the UI polls.
func (s *ClusterOrderService) RecordPhase(ctx context.Context, id, orgID, phase, detail string) error {
	return s.setVendPhase(ctx, id, orgID, phase, detail)
}

// setVendPhase merges one checkpoint into the operation's vend_phases map. It's
// the single helper both the order service and (later) the in-cluster watcher
// use to advance the timeline.
func (s *ClusterOrderService) setVendPhase(ctx context.Context, id, orgID, phase, detail string) error {
	raw, err := vendPhaseFragment(phase, detail, time.Now().UTC())
	if err != nil {
		return err
	}
	return s.queries.SetVendPhase(ctx, id, orgID, raw)
}

// vendPhaseFragment builds the single-key jsonb fragment merged into vend_phases.
// Exactly one key keeps the merge (`vend_phases || fragment`) regressible — it
// overwrites only that phase and leaves the rest of the timeline intact.
func vendPhaseFragment(phase, detail string, at time.Time) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]vendPhase{phase: {At: at, Detail: detail}})
	if err != nil {
		return nil, fmt.Errorf("marshal vend phase: %w", err)
	}
	return raw, nil
}

type vendPhase struct {
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// GetOperation reads an operation row by ID. Used by the worker on job start.
func (s *ClusterOrderService) GetOperation(ctx context.Context, id, orgID string) (repository.ClusterOperation, error) {
	return s.queries.GetClusterOperation(ctx, repository.GetClusterOperationParams{ID: id, OrgID: orgID})
}

// ListOperations returns the per-cluster operation log for the UI panel.
func (s *ClusterOrderService) ListOperations(ctx context.Context, orgID, name, environment string) ([]repository.ClusterOperation, error) {
	return s.queries.ListClusterOperations(ctx, repository.ListClusterOperationsParams{
		OrgID: orgID, Name: name, Environment: environment,
	})
}

// ListAllOperations returns recent cluster operations across the org — the
// Clusters-tab order feed (so in-flight/failed vends are visible without
// having to know the cluster name first).
func (s *ClusterOrderService) ListAllOperations(ctx context.Context, orgID string) ([]repository.ClusterOperation, error) {
	return s.queries.ListClusterOperationsByOrg(ctx, orgID)
}
