package service

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := s.assertNameAvailable(ctx, input.Name); err != nil {
		return repository.ClusterOperation{}, err
	}
	return s.enqueue(ctx, orgID, "provision", createdBy, input)
}

// assertNameAvailable rejects a provision whose cluster name is already vended.
//
// The uniqueness constraint on clusters is the authority, but reaching it costs
// a cluster: the order commits a CR, ArgoCD applies it, Crossplane builds real
// EKS, and only the watch-back's register call meets the constraint — half an
// hour and a billing cluster later. Asking at order time makes the same answer
// free, and it is the answer a user can act on.
//
// Deployment-wide, because the name is (see ClusterNameTaken), so this can
// reject a name held by another org. It reports the environment and nothing
// else: enough to explain the conflict without enumerating anyone's fleet.
//
// A registered cluster is only half the answer. Between the order and the
// register there is a vend window of half an hour or so in which no clusters
// row exists, and a second order landing in it overwrites the first's manifest
// in place — same name, same path — while ArgoCD is mid-build. So an in-flight
// provision holds the name too.
func (s *ClusterOrderService) assertNameAvailable(ctx context.Context, name string) error {
	taken, environment, err := s.queries.ClusterNameTaken(ctx, name)
	if err != nil {
		return fmt.Errorf("check cluster name: %w", err)
	}
	if taken {
		return apperr.Conflict(fmt.Sprintf(
			"a cluster named %q is already registered (environment %s); cluster names are unique per portal deployment, since the clusters repo path, the hub ArgoCD Application and the tenants repo directory all key on the name",
			name, environment))
	}

	building, environment, err := s.queries.ProvisionInFlight(ctx, name)
	if err != nil {
		return fmt.Errorf("check in-flight provisions: %w", err)
	}
	if building {
		return apperr.Conflict(fmt.Sprintf(
			"a cluster named %q is already being vended (environment %s); ordering a second one would rewrite its manifest mid-build",
			name, environment))
	}
	return nil
}

// EnqueueDeprovision records intent to tear a cluster down (remove its file →
// ArgoCD prunes → Crossplane tofu destroy). name+environment locate the manifest;
// team is the Workspace/CR namespace and must match the team the cluster was
// vended under — a wrong team makes reconcileDeprovision Get the XR in the
// wrong namespace, read NotFound as "teardown complete", and flip the op
// terminal on the first tick while destroy never started.
func (s *ClusterOrderService) EnqueueDeprovision(ctx context.Context, orgID, name, environment, team, createdBy string) (repository.ClusterOperation, error) {
	if err := s.assertDeprovisionable(ctx, orgID, name, environment, team); err != nil {
		return repository.ClusterOperation{}, err
	}
	return s.enqueue(ctx, orgID, "deprovision", createdBy, clusterspec.Input{
		Name: name, Environment: environment, Team: team,
	})
}

// assertDeprovisionable rejects a teardown of something portal has no record
// of vending, or a teardown that names the wrong team. Teardown is removal:
// the worker deletes clusters/<environment>/<name>.yaml and lets ArgoCD prune.
// Given the wrong name/environment pair it deletes nothing, and "nothing to
// delete" and "deleted it" leave the same clean tree — which is how a typo
// used to read as a completed teardown. Given the wrong team, the git remove
// may still work (path has no team segment) but the watch-back Gets the XR in
// the wrong namespace and treats NotFound as success.
//
// A committed/active provision is the authoritative record (and carries the
// team). A registered cluster without ops is the other path (hand registration
// / ArgoCD sync) — team cannot be checked there because Cluster rows do not
// store it; those teardowns still require the name+environment match.
func (s *ClusterOrderService) assertDeprovisionable(ctx context.Context, orgID, name, environment, team string) error {
	ops, err := s.queries.ListClusterOperations(ctx, repository.ListClusterOperationsParams{
		OrgID: orgID, Name: name, Environment: environment,
	})
	if err != nil {
		return fmt.Errorf("list operations: %w", err)
	}
	if provision, ok := latestCommittedProvision(ops); ok {
		if err := assertProvisionTeam(name, environment, provision.Team, team); err != nil {
			return err
		}
		return nil
	}

	cluster, err := s.queries.GetClusterByName(ctx, repository.GetClusterByNameParams{OrgID: orgID, Name: name})
	switch {
	case err == nil && cluster.Environment == environment:
		return nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("look up cluster: %w", err)
	}
	return apperr.Conflict(fmt.Sprintf(
		"no cluster %q on record in environment %s; there is no manifest to remove, so this would report a teardown that never happened",
		name, environment))
}

// latestCommittedProvision returns the newest provision op that put a manifest
// in the clusters repo. ListClusterOperations is newest-first.
func latestCommittedProvision(ops []repository.ClusterOperation) (repository.ClusterOperation, bool) {
	for _, op := range ops {
		if op.Operation == "provision" && (op.Status == "committed" || op.Status == "active") {
			return op, true
		}
	}
	return repository.ClusterOperation{}, false
}

// assertProvisionTeam rejects a deprovision/unwedge whose ?team= does not match
// the team the committed provision recorded. Empty provision team skips the
// check (legacy rows); non-empty must match exactly.
func assertProvisionTeam(name, environment, provisionTeam, requestedTeam string) error {
	if provisionTeam == "" || provisionTeam == requestedTeam {
		return nil
	}
	return apperr.Conflict(fmt.Sprintf(
		"cluster %q in environment %s was vended under team %q, not %q; a wrong team would make the teardown report complete while destroy never started",
		name, environment, provisionTeam, requestedTeam))
}

// hasCommittedProvision reports whether any of these operations put a manifest
// in the clusters repo. 'committed' means the worker pushed it; 'active' means
// the vend also finished. 'pending' has not written anything yet, and 'failed'
// and 'deprovisioned' are both states in which no manifest is left to remove.
func hasCommittedProvision(ops []repository.ClusterOperation) bool {
	_, ok := latestCommittedProvision(ops)
	return ok
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
	// Same team gate as deprovision: unwedge Gets the Workspace in op.Team.
	if err := s.assertDeprovisionable(ctx, orgID, name, environment, team); err != nil {
		return repository.ClusterOperation{}, err
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
