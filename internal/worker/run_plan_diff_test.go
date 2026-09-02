package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
)

// An approval authorises an apply against a run row, and the plan diff is the
// artefact it is granted against. `run.plan_json_url` empty is the only thing
// the reading surfaces check — the Changes tab is hidden and the plan-json
// endpoint refuses — so a run parked at awaiting_approval without one asks an
// admin to sign off on changes nothing can show them. The plan text is still on
// the run, which is what makes it look complete.
//
// Four things emptied that column and none announced itself. These tests plant
// each and require the run to refuse rather than park.

type stubPlans struct {
	pointed []repository.UpdateRunPlanJSONURLParams
	fail    error
}

func (s *stubPlans) UpdateRunPlanJSONURL(_ context.Context, arg repository.UpdateRunPlanJSONURLParams) error {
	s.pointed = append(s.pointed, arg)
	return s.fail
}

type planBlobs struct {
	stubBlobs
	failPutPlanJSON error
	stored          [][]byte
}

func (b *planBlobs) PutPlanJSON(_ context.Context, runID string, data []byte) (string, error) {
	b.stored = append(b.stored, data)
	if b.failPutPlanJSON != nil {
		return "", b.failPutPlanJSON
	}
	return "plans/" + runID + "/plan.json", nil
}

func planWorker(plans *stubPlans, blobs *planBlobs) *RunJobWorker {
	w := &RunJobWorker{plans: plans}
	if blobs != nil {
		w.storage = blobs
	}
	return w
}

var samplePlanJSON = []byte(`{"format_version":"1.2","resource_changes":[]}`)

// ── the four ways the column stayed empty ──────────────────────────────────

func TestRecordPlanDiff_ReportsEveryWayTheDiffGoesMissing(t *testing.T) {
	cases := []struct {
		name     string
		planJSON []byte
		plans    *stubPlans
		blobs    *planBlobs
		wantIn   string
	}{
		{
			name:     "the executor generated none",
			planJSON: nil,
			plans:    &stubPlans{},
			blobs:    &planBlobs{},
			wantIn:   "no JSON plan",
		},
		{
			name:     "the upload failed",
			planJSON: samplePlanJSON,
			plans:    &stubPlans{},
			blobs:    &planBlobs{failPutPlanJSON: errors.New("503 SlowDown")},
			wantIn:   "503 SlowDown",
		},
		{
			name:     "the row that points at it failed",
			planJSON: samplePlanJSON,
			plans:    &stubPlans{fail: errors.New("deadlock detected")},
			blobs:    &planBlobs{},
			wantIn:   "deadlock detected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := planWorker(tc.plans, tc.blobs).recordPlanDiff(context.Background(), runArgs(), tc.planJSON)
			if err == nil {
				t.Fatalf("a run with no diff reported none missing, so it would have parked for approval")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the cause is not in the error: %v", err)
			}
		})
	}

	// The fourth is the instance storing no artefacts at all, which is a
	// configuration rather than a failure and is named separately.
	err := planWorker(&stubPlans{}, nil).recordPlanDiff(context.Background(), runArgs(), samplePlanJSON)
	if !errors.Is(err, errNoArtifactStorage) {
		t.Errorf("an instance with no object storage reports %v, want errNoArtifactStorage", err)
	}
}

// The upload landing and the row not landing leaves the diff in object storage
// with nothing referencing it. The message has to say where, because the
// recovery differs from a failed upload.
func TestRecordPlanDiff_SaysTheDiffIsStoredButUnreachable(t *testing.T) {
	err := planWorker(&stubPlans{fail: errors.New("deadlock detected")}, &planBlobs{}).
		recordPlanDiff(context.Background(), runArgs(), samplePlanJSON)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "plans/run_1/plan.json") {
		t.Errorf("the error does not say where the unreachable diff is: %v", err)
	}
}

func TestRecordPlanDiff_PointsTheRunAtTheStoredDiff(t *testing.T) {
	plans := &stubPlans{}
	blobs := &planBlobs{}
	if err := planWorker(plans, blobs).recordPlanDiff(context.Background(), runArgs(), samplePlanJSON); err != nil {
		t.Fatalf("recordPlanDiff: %v", err)
	}
	if len(blobs.stored) != 1 || string(blobs.stored[0]) != string(samplePlanJSON) {
		t.Fatalf("stored %v, want the plan JSON once", blobs.stored)
	}
	if len(plans.pointed) != 1 || plans.pointed[0].PlanJSONURL != "plans/run_1/plan.json" {
		t.Errorf("pointed at %v", plans.pointed)
	}
}

// ── which absences refuse an approval ──────────────────────────────────────

// awaiting_approval is the one status where the diff is the point. The others
// lose a view: "planned" loses a tab on a run nobody signs, "queued" applies
// without a human reading anything, and an apply never had a diff to lose.
func TestMissingDiffBlocksApproval_OnlyWhereAnApprovalIsBeingAskedFor(t *testing.T) {
	boom := errors.New("503 SlowDown")
	cases := []struct {
		operation, finalStatus string
		want                   bool
		why                    string
	}{
		{"plan", "awaiting_approval", true, "an admin is being asked to sign off on a diff nothing can show them"},
		{"plan", "queued", false, "auto-apply follows without a human reading anything"},
		{"plan", "planned", false, "nobody is signing this run"},
		{"apply", "applied", false, "an apply produces no JSON plan, so it has none to lose"},
		{"destroy", "applied", false, "same"},
	}
	for _, tc := range cases {
		if got := missingDiffBlocksApproval(tc.operation, tc.finalStatus, boom); got != tc.want {
			t.Errorf("missingDiffBlocksApproval(%q, %q) = %v, want %v — %s", tc.operation, tc.finalStatus, got, tc.want, tc.why)
		}
	}
}

// An instance that stores no artefacts refuses nothing: every run on it would
// park without a diff, and failing them all would make approval-gated
// workspaces unusable on a configuration portal supports.
func TestMissingDiffBlocksApproval_ExemptsAnInstanceWithNoArtifactStorage(t *testing.T) {
	if missingDiffBlocksApproval("plan", "awaiting_approval", errNoArtifactStorage) {
		t.Error("an instance with no object storage refused an approval; every approval-gated workspace on it would be unusable")
	}
	if reportsMissingDiff("plan", errNoArtifactStorage) {
		t.Error("an instance with no object storage annotates every plan with a line its operator cannot act on")
	}
	if !reportsMissingDiff("plan", errors.New("503 SlowDown")) {
		t.Error("a plan whose diff was lost says nothing")
	}
	if reportsMissingDiff("apply", errors.New("503 SlowDown")) {
		t.Error("an apply was reported as having lost a diff it never had")
	}
}

// More than one thing can be lost by one run, and dropping all but the first
// would make what an operator reads depend on the order the failures happened
// in.
func TestJoinRunNotices_KeepsEveryNotice(t *testing.T) {
	if joinRunNotices(nil) != nil {
		t.Error("a clean run carries a message")
	}
	joined := joinRunNotices([]string{"the state was not recorded.", "the diff was not recorded."})
	if joined == nil {
		t.Fatal("two notices produced none")
	}
	for _, want := range []string{"the state was not recorded.", "the diff was not recorded."} {
		if !strings.Contains(*joined, want) {
			t.Errorf("the joined notice dropped %q: %s", want, *joined)
		}
	}
}

// ── the refusal reaching the run an operator opens ─────────────────────────

func seedPlanRun(t *testing.T, ctx context.Context, tag string, requiresApproval bool) (string, string, repository.Run) {
	t.Helper()
	orgID, userID := seedOrg(t, ctx, tag)

	wsID := id()
	gate := "FALSE"
	if requiresApproval {
		gate = "TRUE"
	}
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version,requires_approval)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0',`+gate+`)`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return orgID, wsID, run
}

func TestRunJobRefusesToParkForApprovalWithNoDiff(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, wsID, run := seedPlanRun(t, ctx, "nodiff", true)

	// The plan succeeds and its diff never reaches object storage.
	w := newTestRunWorker(&recordingExecutor{})
	w.plans = &stubPlans{}
	w.storage = &planBlobs{failPutPlanJSON: errors.New("503 SlowDown")}
	w.states = &stubStates{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status == "awaiting_approval" {
		t.Fatal("the run parked for approval with no diff; an admin would be asked to sign off on changes nothing can show them")
	}
	if finished.Status != "errored" {
		t.Errorf("status = %q, want errored", finished.Status)
	}
	for _, want := range []string{"approval", "503 SlowDown", "no diff to approve"} {
		if !strings.Contains(finished.ErrorMessage, want) {
			t.Errorf("the run's message is missing %q:\n%s", want, finished.ErrorMessage)
		}
	}
}

// A plan whose diff was generated and stored parks for approval as before. The
// refusal above means nothing if this does not hold.
func TestRunJobParksForApprovalWithADiff(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, wsID, run := seedPlanRun(t, ctx, "withdiff", true)

	plans := &stubPlans{}
	w := newTestRunWorker(&recordingExecutor{})
	w.plans = plans
	w.storage = &planBlobs{}
	w.states = &stubStates{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "awaiting_approval" {
		t.Errorf("status = %q, want awaiting_approval", finished.Status)
	}
	if finished.ErrorMessage != "" {
		t.Errorf("a plan with a diff carries a message:\n%s", finished.ErrorMessage)
	}
	if len(plans.pointed) != 1 {
		t.Errorf("the run was pointed at %d diffs, want 1", len(plans.pointed))
	}
}

// A plan that nobody has to sign keeps its status and says what is missing. The
// tab is empty either way; the difference is whether the run explains it.
func TestRunJobReportsAMissingDiffOnAnUngatedPlan(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, wsID, run := seedPlanRun(t, ctx, "ungated", false)

	w := newTestRunWorker(&recordingExecutor{omitPlanJSON: true})
	w.plans = &stubPlans{}
	w.storage = &planBlobs{}
	w.states = &stubStates{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "planned" {
		t.Errorf("status = %q, want planned; the plan itself succeeded", finished.Status)
	}
	for _, want := range []string{"plan succeeded", "no machine-readable diff", "Changes tab"} {
		if !strings.Contains(finished.ErrorMessage, want) {
			t.Errorf("the run's message is missing %q:\n%s", want, finished.ErrorMessage)
		}
	}
}
