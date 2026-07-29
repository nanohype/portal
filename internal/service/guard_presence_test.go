package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/clusterspec"
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
	ops        []repository.ClusterOperation
	cluster    repository.Cluster
	found      bool
	listErr    error
	calls      int
	nameTaken  bool
	inFlight   bool
	account    repository.Account
	accountErr error
	acctCalls  int
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

func (s *stubClusterRecords) ClusterNameTaken(_ context.Context, _ string) (bool, string, error) {
	return s.nameTaken, "production", nil
}

func (s *stubClusterRecords) ProvisionInFlight(_ context.Context, _ string) (bool, string, error) {
	return s.inFlight, "production", nil
}

func (s *stubClusterRecords) GetAccountByAWSID(_ context.Context, _ repository.GetAccountByAWSIDParams) (repository.Account, error) {
	s.acctCalls++
	if s.accountErr != nil {
		return repository.Account{}, s.accountErr
	}
	return s.account, nil
}

func deprovision(records clusterRecordLookup, name, environment, team string) error {
	svc := &ClusterOrderService{records: records}
	_, err := svc.EnqueueDeprovision(context.Background(), "org_1", name, environment, team, "user_1")
	return err
}

func provision(records clusterRecordLookup, in clusterspec.Input) error {
	svc := &ClusterOrderService{records: records}
	_, err := svc.EnqueueProvision(context.Background(), "org_1", "user_1", in)
	return err
}

func vendOrder() clusterspec.Input {
	return clusterspec.Input{
		Name: "analytics", Account: "222222222222", Region: "us-east-1", Team: "platform",
	}
}

// The vend's own gates. Same reasoning as the teardown ones above: each rule is
// unit-tested elsewhere, and none of those tests notices when the call is gone
// from EnqueueProvision.

func TestEnqueueProvision_RefusesANameAlreadyVended(t *testing.T) {
	err := provision(&stubClusterRecords{nameTaken: true}, vendOrder())

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict — the name gate is not on the provision path", apperr.KindOf(err))
	}
}

func TestEnqueueProvision_RefusesANameAlreadyBeingVended(t *testing.T) {
	// The in-flight window is the dangerous one: no clusters row exists yet, so
	// only this gate stands between a second order and a manifest rewritten
	// under a build in progress.
	err := provision(&stubClusterRecords{inFlight: true}, vendOrder())

	if apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("kind = %v, want Conflict for an in-flight provision", apperr.KindOf(err))
	}
}

func TestEnqueueProvision_StampsTheOrderingAccountsSpokeRole(t *testing.T) {
	// Without the stamp cluster-stack grants portal no EKS access entry, and
	// portal cannot authenticate to the kube API of a cluster it vended itself —
	// no token minting, no tenant watch. Nothing about that failure surfaces at
	// order time, so the presence of the call is what has to be pinned.
	//
	// The service's pool stays nil: reaching the DB write means the stamp
	// already ran, so the panic recovered below is the success signal.
	records := &stubClusterRecords{
		account: repository.Account{AssumeRoleARN: "arn:aws:iam::222222222222:role/production-portal-spoke"},
	}
	func() {
		defer func() { _ = recover() }()
		_ = provision(records, vendOrder())
	}()

	if records.acctCalls == 0 {
		t.Error("EnqueueProvision never resolved the ordering account, so the vend carries no portalAccessRoleArn")
	}
}

func TestEnqueueProvision_RefusesAnUnregisteredAWSAccount(t *testing.T) {
	// The watch-back resolves the same row to register the finished cluster, so
	// this vend could not complete anyway — it would just fail half an hour and
	// one billing cluster later.
	err := provision(&stubClusterRecords{accountErr: pgx.ErrNoRows}, vendOrder())

	if apperr.KindOf(err) != apperr.KindValidation {
		t.Fatalf("kind = %v, want Validation for an unregistered AWS account", apperr.KindOf(err))
	}
}

func TestEnqueueProvision_SurfacesAnAccountLookupFailure(t *testing.T) {
	err := provision(&stubClusterRecords{accountErr: errors.New("connection refused")}, vendOrder())

	if err == nil {
		t.Fatal("a failed account lookup was treated as a vend with no spoke role")
	}
	if apperr.KindOf(err) == apperr.KindValidation {
		t.Errorf("a lookup failure should not read as a bad order: %v", err)
	}
}

func TestEnqueueProvision_ValidatesBeforeTouchingTheDatabase(t *testing.T) {
	// A cross-account vend with no permissions boundaries renders a CR the
	// Cluster XRD rejects at admission — after portal has committed it and
	// reported the operation committed. Caught here, it is a 400 on the form.
	in := vendOrder()
	in.VendRoleArn = "arn:aws:iam::222222222222:role/production-eks-fleet-vend"

	records := &stubClusterRecords{}
	err := provision(records, in)

	if err == nil {
		t.Fatal("a vend role with no permissions boundary was accepted")
	}
	if records.calls != 0 || records.acctCalls != 0 {
		t.Error("validation ran after the record lookups rather than before them")
	}
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
