package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
)

// Guards on the write path.
//
// Each gate below was extracted into a pure function and unit-tested there,
// which pins the *rules*. It does not pin the *call*: delete
// `assertTenantNameFree(...)` from EnqueueCreate, or `assertDeprovisionable(...)`
// from EnqueueDeprovision, and every existing test still passes while the
// protection is gone.
//
// These tests put a blocking condition in front of each entry point and require
// it to be refused there. The service's pool and River client stay nil on
// purpose: a create or teardown that reaches the DB write has already passed the
// gate, so a panic is the correct outcome and the test says so rather than
// reporting a pass.

type stubTenantNames struct {
	exists     bool
	pending    bool
	lookupErr  error
	pendingErr error
	calls      int
}

func (s *stubTenantNames) GetTenantByClusterAndName(_ context.Context, _, _, _ string) (repository.Tenant, error) {
	s.calls++
	if s.lookupErr != nil {
		return repository.Tenant{}, s.lookupErr
	}
	if s.exists {
		return repository.Tenant{Name: "acme"}, nil
	}
	return repository.Tenant{}, pgx.ErrNoRows
}

func (s *stubTenantNames) HasPendingTenantCreate(_ context.Context, _, _, _ string) (bool, error) {
	if s.pendingErr != nil {
		return false, s.pendingErr
	}
	return s.pending, nil
}

func tenantCreate(names tenantNameLookup) (repository.TenantOperation, error) {
	svc := &TenantService{names: names}
	return svc.EnqueueCreate(context.Background(), CreateTenantInput{
		OrgID:        "org_1",
		ClusterID:    "cluster_1",
		Name:         "acme",
		OwningTeamID: "team_1",
		CreatedBy:    "user_1",
	})
}

func TestEnqueueCreate_RefusesAnExistingTenant(t *testing.T) {
	// The worker's git write is unconditional. Without this gate on the path, a
	// create against a live name replaces another team's Platform — budget,
	// datastores, capabilities — and grants the caller access to the result.
	names := &stubTenantNames{exists: true}
	_, err := tenantCreate(names)

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict — the create gate is not on the write path", apperr.KindOf(err))
	}
	if names.calls == 0 {
		t.Error("EnqueueCreate never consulted the inventory")
	}
}

func TestEnqueueCreate_RefusesWhileACreateIsPending(t *testing.T) {
	// The inventory is only written by the watcher, so a create enqueued
	// seconds ago is invisible there — the pending-op check is the only thing
	// standing between two operators and two conflicting commits.
	_, err := tenantCreate(&stubTenantNames{pending: true})

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict", apperr.KindOf(err))
	}
}

func TestEnqueueCreate_SurfacesALookupFailureRatherThanProceeding(t *testing.T) {
	// A gate that cannot read cannot clear. Treating a failed lookup as "name
	// is free" would overwrite a live Platform precisely when the database is
	// already unhealthy.
	_, err := tenantCreate(&stubTenantNames{lookupErr: errors.New("connection refused")})
	if err == nil {
		t.Fatal("a failed inventory lookup was treated as a free name")
	}
	if apperr.KindOf(err) == apperr.KindConflict {
		t.Errorf("a lookup failure should not read as a conflict: %v", err)
	}

	_, err = tenantCreate(&stubTenantNames{pendingErr: errors.New("connection refused")})
	if err == nil {
		t.Fatal("a failed pending-op lookup was treated as no pending create")
	}
}

func TestEnqueueCreate_ValidatesBeforeTouchingTheDatabase(t *testing.T) {
	// Validation runs first so a bad form never creates a dangling operation
	// row. If it moved after the gate, this would reach the nil lookup.
	svc := &TenantService{}
	_, err := svc.EnqueueCreate(context.Background(), CreateTenantInput{OrgID: "org_1"})

	if err == nil {
		t.Fatal("an input with no cluster or name was accepted")
	}
}

type stubClusterRecords struct {
	ops     []repository.ClusterOperation
	cluster repository.Cluster
	found   bool
	listErr error
	calls   int
}

func (s *stubClusterRecords) ListClusterOperations(_ context.Context, _ repository.ListClusterOperationsParams) ([]repository.ClusterOperation, error) {
	s.calls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.ops, nil
}

func (s *stubClusterRecords) GetClusterByName(_ context.Context, _ repository.GetClusterByNameParams) (repository.Cluster, error) {
	if !s.found {
		return repository.Cluster{}, pgx.ErrNoRows
	}
	return s.cluster, nil
}

func deprovision(records clusterRecordLookup, name, environment, team string) error {
	svc := &ClusterOrderService{records: records}
	_, err := svc.EnqueueDeprovision(context.Background(), "org_1", name, environment, team, "user_1")
	return err
}

func TestEnqueueDeprovision_RefusesAClusterPortalNeverVended(t *testing.T) {
	// Teardown removes clusters/<env>/<name>.yaml. Given a name portal has no
	// record of, it deletes nothing — and "nothing to delete" leaves the same
	// clean tree as "deleted it", so a typo reports a completed teardown of a
	// cluster that is still running.
	records := &stubClusterRecords{}
	err := deprovision(records, "typo-cluster", "production", "growth")

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict — the teardown gate is not on the path", apperr.KindOf(err))
	}
	if records.calls == 0 {
		t.Error("EnqueueDeprovision never consulted the operation history")
	}
}

func TestEnqueueDeprovision_RefusesTheWrongTeam(t *testing.T) {
	// The git remove succeeds either way — the path carries no team segment —
	// but the watch-back Gets the XR in the wrong namespace, reads NotFound as
	// "teardown complete", and flips the op terminal while destroy never ran.
	records := &stubClusterRecords{ops: []repository.ClusterOperation{
		{Operation: "provision", Status: "committed", Name: "hub", Environment: "production", Team: "platform"},
	}}
	err := deprovision(records, "hub", "production", "growth")

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict for a team mismatch", apperr.KindOf(err))
	}
}

func TestEnqueueDeprovision_RefusesTheWrongEnvironment(t *testing.T) {
	// Same failure shape as a wrong name: the manifest lives under the
	// environment directory, so a mismatched pair removes nothing.
	records := &stubClusterRecords{
		found:   true,
		cluster: repository.Cluster{Name: "hub", Environment: "staging"},
	}
	err := deprovision(records, "hub", "production", "growth")

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict for an environment mismatch", apperr.KindOf(err))
	}
}

func TestEnqueueDeprovision_SurfacesALookupFailure(t *testing.T) {
	err := deprovision(&stubClusterRecords{listErr: errors.New("connection refused")}, "hub", "production", "growth")

	if err == nil {
		t.Fatal("a failed history lookup was treated as an authorized teardown")
	}
	if apperr.KindOf(err) == apperr.KindConflict {
		t.Errorf("a lookup failure should not read as a conflict: %v", err)
	}
}
