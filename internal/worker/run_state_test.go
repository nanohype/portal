package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// A run's relationship with state has two halves and both were silent.
//
// Before executing, a workspace that has state and cannot produce it planned
// against an empty one: an apply then re-creates everything, and the run log is
// an ordinary log of a workspace being built. After executing, state that could
// not be stored — or filed at a guessed serial — left the workspace's recorded
// state describing infrastructure that no longer exists, while the run row
// carried the correct resource counts.
//
// These tests plant each failure and require the run to refuse before it
// executes, or to say both halves after it has.

type stubStates struct {
	latest     repository.StateVersion
	failLatest error

	created    []repository.CreateStateVersionParams
	failCreate error
}

func (s *stubStates) GetLatestStateVersion(context.Context, repository.GetLatestStateVersionParams) (repository.StateVersion, error) {
	return s.latest, s.failLatest
}

func (s *stubStates) CreateStateVersion(_ context.Context, arg repository.CreateStateVersionParams) (repository.StateVersion, error) {
	s.created = append(s.created, arg)
	if s.failCreate != nil {
		return repository.StateVersion{}, s.failCreate
	}
	return repository.StateVersion{Serial: arg.Serial, StateURL: arg.StateURL}, nil
}

type stubBlobs struct {
	rawState, browseState []byte
	failGetRaw            error
	failGetBrowse         error
	failPutRaw            error
	failPutState          error

	putRawSerials, putStateSerials []int
}

func (s *stubBlobs) GetRawState(context.Context, string) ([]byte, error) {
	return s.rawState, s.failGetRaw
}
func (s *stubBlobs) GetState(context.Context, string) ([]byte, error) {
	return s.browseState, s.failGetBrowse
}
func (s *stubBlobs) PutRawState(_ context.Context, _ string, serial int, _ []byte) (string, error) {
	s.putRawSerials = append(s.putRawSerials, serial)
	return "state-raw/ws_1", s.failPutRaw
}
func (s *stubBlobs) PutState(_ context.Context, _ string, serial int, _ []byte) (string, error) {
	s.putStateSerials = append(s.putStateSerials, serial)
	return "state/ws_1", s.failPutState
}
func (s *stubBlobs) GetConfigArchive(context.Context, string) ([]byte, error) { return nil, nil }
func (s *stubBlobs) PutLog(context.Context, string, string, []byte) (string, error) {
	return "", nil
}
func (s *stubBlobs) PutPlanJSON(context.Context, string, []byte) (string, error) { return "", nil }

func stateWorker(states *stubStates, blobs *stubBlobs) *RunJobWorker {
	w := &RunJobWorker{states: states}
	if blobs != nil {
		w.storage = blobs
	}
	return w
}

func runArgs() RunJobArgs {
	return RunJobArgs{RunID: "run_1", WorkspaceID: "ws_1", OrgID: "org_1", Operation: "apply"}
}

// ── restoring the state a run continues from ───────────────────────────────

func restore(t *testing.T, states *stubStates, blobs *stubBlobs) ([]byte, error) {
	t.Helper()
	var buf strings.Builder
	return stateWorker(states, blobs).restorePreviousState(context.Background(), runArgs(), capture(&buf))
}

// The read that answers "does this workspace have state" is the one that must
// not fail open: its failure and the answer "none yet" produced the same empty
// state on the next line.
func TestRestorePreviousState_RefusesWhenTheLatestVersionCannotBeRead(t *testing.T) {
	_, err := restore(t, &stubStates{failLatest: errors.New("connection reset by peer")}, &stubBlobs{})
	if err == nil {
		t.Fatal("the run proceeded with no state although whether it has any could not be determined; an apply would re-create every resource")
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("the cause is not in the error: %v", err)
	}
}

func TestRestorePreviousState_RefusesWhenTheRecordedStateCannotBeFetched(t *testing.T) {
	states := &stubStates{latest: repository.StateVersion{Serial: 47, StateURL: "state/ws_1/47.tfstate"}}
	blobs := &stubBlobs{
		failGetRaw:    errors.New("NoSuchKey"),
		failGetBrowse: errors.New("503 SlowDown"),
	}
	_, err := restore(t, states, blobs)
	if err == nil {
		t.Fatal("a workspace recorded at serial 47 executed against an empty state")
	}
	for _, want := range []string{"47", "NoSuchKey", "503 SlowDown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q, so an operator cannot tell which object is missing: %v", want, err)
		}
	}
}

// A state version row that points at no browse object and whose raw object is
// gone is a row pointing at nothing. Reading it as "no state" hides that.
func TestRestorePreviousState_RefusesWhenTheRowPointsAtNothing(t *testing.T) {
	states := &stubStates{latest: repository.StateVersion{Serial: 12, StateURL: ""}}
	_, err := restore(t, states, &stubBlobs{failGetRaw: errors.New("NoSuchKey")})
	if err == nil {
		t.Fatal("a state version with no reachable object was treated as no state at all")
	}
	if !strings.Contains(err.Error(), "12") {
		t.Errorf("the error does not name the serial: %v", err)
	}
}

// A workspace acquires state versions only through a run that stored them, so
// this is an instance that had object storage and lost it.
func TestRestorePreviousState_RefusesWhenStorageIsGoneButStateIsRecorded(t *testing.T) {
	states := &stubStates{latest: repository.StateVersion{Serial: 3, StateURL: "state/ws_1/3.tfstate"}}
	_, err := restore(t, states, nil)
	if err == nil {
		t.Fatal("a workspace with recorded state ran with no object storage and no complaint")
	}
	if !strings.Contains(err.Error(), "object storage") {
		t.Errorf("the error does not say storage is absent: %v", err)
	}
}

// The ordinary cases must not be refused, or every first run of every workspace
// fails.
func TestRestorePreviousState_AcceptsAWorkspaceWithNoStateYet(t *testing.T) {
	for _, states := range []*stubStates{
		{failLatest: pgx.ErrNoRows},
		{latest: repository.StateVersion{Serial: 0}},
	} {
		state, err := restore(t, states, &stubBlobs{})
		if err != nil {
			t.Errorf("a workspace with no state was refused: %v", err)
		}
		if state != nil {
			t.Errorf("got %d bytes of state for a workspace that has none", len(state))
		}
	}
}

func TestRestorePreviousState_PrefersRawAndFallsBackToBrowse(t *testing.T) {
	states := &stubStates{latest: repository.StateVersion{Serial: 47, StateURL: "state/ws_1/47.tfstate"}}

	raw, err := restore(t, states, &stubBlobs{rawState: []byte("raw"), browseState: []byte("browse")})
	if err != nil || string(raw) != "raw" {
		t.Errorf("got %q, %v; want the raw object, which preserves backend encryption", raw, err)
	}

	// Terragrunt workspaces never write a raw object, so the fallback is their
	// ordinary path rather than a recovery.
	browse, err := restore(t, states, &stubBlobs{failGetRaw: errors.New("NoSuchKey"), browseState: []byte("browse")})
	if err != nil || string(browse) != "browse" {
		t.Errorf("got %q, %v; want the browse object", browse, err)
	}
}

// ── recording the state a run produced ─────────────────────────────────────

func record(t *testing.T, states *stubStates, blobs *stubBlobs, out stateOutcome) error {
	t.Helper()
	return stateWorker(states, blobs).recordStateVersion(context.Background(), runArgs(), out)
}

func applied() stateOutcome {
	return stateOutcome{StateFile: []byte("tfstate"), StateJSON: []byte("json"), ResourceCount: 12, ResourceSummary: "+12 ~0 -0"}
}

// The serial reset is the sharpest failure on this path: a discarded read makes
// the next serial 1, which overwrites the workspace's oldest state objects and
// files a row that sorts below the real latest.
func TestRecordStateVersion_NeverWritesAtAGuessedSerial(t *testing.T) {
	states := &stubStates{failLatest: errors.New("connection reset by peer")}
	blobs := &stubBlobs{}

	err := record(t, states, blobs, applied())
	if err == nil {
		t.Fatal("the state was filed although the serial to file it at could not be read")
	}
	if len(blobs.putRawSerials)+len(blobs.putStateSerials) != 0 {
		t.Errorf("objects were written at serials %v/%v; an unknown serial must write nothing rather than overwrite serial 1",
			blobs.putRawSerials, blobs.putStateSerials)
	}
	if len(states.created) != 0 {
		t.Errorf("a state version row was filed at serial %d", states.created[0].Serial)
	}
}

func TestRecordStateVersion_FilesAtOneAboveTheLatest(t *testing.T) {
	states := &stubStates{latest: repository.StateVersion{Serial: 47}}
	blobs := &stubBlobs{}

	if err := record(t, states, blobs, applied()); err != nil {
		t.Fatalf("recordStateVersion: %v", err)
	}
	if len(states.created) != 1 || states.created[0].Serial != 48 {
		t.Fatalf("filed %v, want one row at serial 48", states.created)
	}
	if states.created[0].ResourceSummary != "+12 ~0 -0" {
		t.Errorf("summary = %q", states.created[0].ResourceSummary)
	}
}

// A first state version is serial 1, and pgx.ErrNoRows is how that is said.
func TestRecordStateVersion_FilesTheFirstVersionAtOne(t *testing.T) {
	states := &stubStates{failLatest: pgx.ErrNoRows}
	if err := record(t, states, &stubBlobs{}, applied()); err != nil {
		t.Fatalf("recordStateVersion: %v", err)
	}
	if len(states.created) != 1 || states.created[0].Serial != 1 {
		t.Fatalf("filed %v, want one row at serial 1", states.created)
	}
}

// Each write on this path is a place the record can be lost. Every one of them
// has to reach the caller, which is the only place that knows whether
// infrastructure changed.
func TestRecordStateVersion_ReportsEveryWriteThatFails(t *testing.T) {
	cases := []struct {
		name   string
		states *stubStates
		blobs  *stubBlobs
		wantIn string
	}{
		{
			name:   "the raw object",
			states: &stubStates{latest: repository.StateVersion{Serial: 47}},
			blobs:  &stubBlobs{failPutRaw: errors.New("503 SlowDown")},
			wantIn: "503 SlowDown",
		},
		{
			name:   "the browse object",
			states: &stubStates{latest: repository.StateVersion{Serial: 47}},
			blobs:  &stubBlobs{failPutState: errors.New("credentials expired")},
			wantIn: "credentials expired",
		},
		{
			name:   "the row that points at them",
			states: &stubStates{latest: repository.StateVersion{Serial: 47}, failCreate: errors.New("duplicate key")},
			blobs:  &stubBlobs{},
			wantIn: "duplicate key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := record(t, tc.states, tc.blobs, applied())
			if err == nil {
				t.Fatalf("a failure writing %s was not reported, so the run would have recorded success", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the cause is not in the error: %v", err)
			}
		})
	}
}

// The blob landing and the row not landing is the worst of the three: the state
// exists and nothing can reach it. The message has to say so, because the
// recovery is different from a failed upload.
func TestRecordStateVersion_SaysTheStateIsStoredButUnreachable(t *testing.T) {
	states := &stubStates{latest: repository.StateVersion{Serial: 47}, failCreate: errors.New("duplicate key")}
	err := record(t, states, &stubBlobs{}, applied())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "48") || !strings.Contains(err.Error(), "state/ws_1") {
		t.Errorf("the error does not say where the unreachable state is: %v", err)
	}
}

func TestRecordStateVersion_ReportsAbsentStorage(t *testing.T) {
	err := record(t, &stubStates{}, nil, applied())
	if err == nil {
		t.Fatal("the state a run produced was dropped with no error because storage is absent")
	}
	if !errors.Is(err, errNoArtifactStorage) {
		t.Errorf("the error does not say storage is absent: %v", err)
	}
}

// ── the message an operator reads ──────────────────────────────────────────

// The instruction this holds: a path that must carry both halves says both. A
// message with only the failure reads as a failed apply and sends the operator
// looking for infrastructure that was not created. A message with only the
// status hides that the workspace's recorded state is stale.
func TestStateRecordFailure_NamesBothHalves(t *testing.T) {
	msg := stateRecordFailure("apply", "+12 ~3 -0", errors.New("duplicate key"))

	changed := []string{"apply", "succeeded", "changed infrastructure", "+12 ~3 -0"}
	lost := []string{"not recorded", "duplicate key", "predates this run", "next run"}

	for _, want := range changed {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say infrastructure changed — missing %q:\n%s", want, msg)
		}
	}
	for _, want := range lost {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say the record was lost — missing %q:\n%s", want, msg)
		}
	}
}

// The operations that change what exists are the ones an unrecorded state loses
// something for. The list is startedStatusFor's mutating set minus "test",
// which runs a smoke script and produces no state of its own; asserting against
// that function rather than restating it is what keeps the two from drifting.
func TestMutatesInfrastructure_MatchesTheApplyingOperationsWithoutTest(t *testing.T) {
	for _, op := range []string{"apply", "destroy", "import"} {
		if !mutatesInfrastructure(op) {
			t.Errorf("%q changes what exists; a state it fails to record is a loss", op)
		}
		if startedStatusFor(op) != "applying" {
			t.Errorf("startedStatusFor(%q) = %q, so the two lists have drifted", op, startedStatusFor(op))
		}
	}
	for _, op := range []string{"test", "plan", ""} {
		if mutatesInfrastructure(op) {
			t.Errorf("%q produces no state of its own; reporting a lost record for it is a false alarm", op)
		}
	}
}

// ── the message reaching the run an operator opens ─────────────────────────

// The composed message is one thing; putting it on the run row is another, and
// only the second is what an operator sees. RunView renders error_message
// whatever the status, so a run that finished as "applied" with this message
// shows both. This drives the whole job to assert that it lands.

type statefulExecutor struct {
	added, changed, deleted int32
}

func (e *statefulExecutor) Execute(_ context.Context, params executor.ExecuteParams) (*executor.ExecuteResult, error) {
	params.LogCallback([]byte("applied\n"))
	return &executor.ExecuteResult{
		Output:           "applied",
		StateFile:        []byte(`{"version":4}`),
		StateJSON:        []byte(`{"version":4}`),
		ResourcesAdded:   e.added,
		ResourcesChanged: e.changed,
		ResourcesDeleted: e.deleted,
	}, nil
}

func TestRunJobReportsBothHalvesWhenTheStateRecordIsLost(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "staterec")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "apply", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	w := newTestRunWorker(&statefulExecutor{added: 12, changed: 3})
	// The apply succeeds and the row that would record its state does not land.
	w.states = &stubStates{latest: repository.StateVersion{Serial: 47}, failCreate: errors.New("duplicate key")}
	w.storage = &stubBlobs{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}

	// Half one: the apply happened, and the run must not deny it.
	if finished.Status != "applied" {
		t.Errorf("status = %q, want applied; the apply did change infrastructure and recording it as anything else sends the operator looking for a failure that did not happen", finished.Status)
	}
	if finished.ResourcesAdded != 12 || finished.ResourcesChanged != 3 {
		t.Errorf("resource counts = +%d ~%d, want +12 ~3", finished.ResourcesAdded, finished.ResourcesChanged)
	}

	// Half two: the record was lost, and the run must say so where it is read.
	if finished.ErrorMessage == "" {
		t.Fatal("the run finished clean although its state version was never written; the workspace's recorded state now predates this run and nothing says so")
	}
	for _, want := range []string{"apply", "changed infrastructure", "+12 ~3 -0", "not recorded", "duplicate key", "next run"} {
		if !strings.Contains(finished.ErrorMessage, want) {
			t.Errorf("the run's message is missing %q, so it carries only one half:\n%s", want, finished.ErrorMessage)
		}
	}
}

// A run whose state records cleanly must carry no message, or the one above
// means nothing.
func TestRunJobReportsNothingWhenTheStateRecordLands(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "stateok")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "apply", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	states := &stubStates{latest: repository.StateVersion{Serial: 47}}
	w := newTestRunWorker(&statefulExecutor{added: 12, changed: 3})
	w.states = states
	w.storage = &stubBlobs{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.ErrorMessage != "" {
		t.Errorf("a run whose state recorded cleanly carries a message:\n%s", finished.ErrorMessage)
	}
	if len(states.created) != 1 || states.created[0].Serial != 48 {
		t.Errorf("filed %v, want one row at serial 48", states.created)
	}
}

// The run refuses before executing when the state it is entitled to cannot be
// produced. Nothing has changed at that point, so refusing costs a re-run and
// nothing else — and the executor must not have been called.
func TestRunJobRefusesBeforeExecutingWhenStateCannotBeRestored(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "staterestore")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "apply", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	rec := &recordingExecutor{}
	w := newTestRunWorker(rec)
	w.states = &stubStates{latest: repository.StateVersion{Serial: 47, StateURL: "state/ws/47.tfstate"}}
	w.storage = &stubBlobs{failGetRaw: errors.New("NoSuchKey"), failGetBrowse: errors.New("503 SlowDown")}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if rec.called {
		t.Error("the executor ran against an empty state for a workspace recorded at serial 47")
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "errored" {
		t.Errorf("status = %q, want errored", finished.Status)
	}
	if !strings.Contains(finished.ErrorMessage, "47") {
		t.Errorf("the run does not say which state version could not be read: %q", finished.ErrorMessage)
	}
}

// The executors read terraform.tfstate and `state pull` behind `err == nil`
// guards, and the Kubernetes executor has no branch at all for some operations,
// so a capture that never happened arrives here as absence rather than as an
// error. A mutating run that produced no state is a change with no record of
// what changed, and it reads on the run exactly like the lost-record case.
func TestRunJobReportsBothHalvesWhenAMutatingRunProducedNoState(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "nostate")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "import", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	states := &stubStates{failLatest: pgx.ErrNoRows}
	w := newTestRunWorker(&recordingExecutor{})
	w.states = states
	w.storage = &stubBlobs{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "import"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "applied" {
		t.Errorf("status = %q, want applied", finished.Status)
	}
	if finished.ErrorMessage == "" {
		t.Fatal("an import recorded as applied captured no state at all and said nothing; nothing records what it changed")
	}
	for _, want := range []string{"import", "changed infrastructure", "captured neither", "next run"} {
		if !strings.Contains(finished.ErrorMessage, want) {
			t.Errorf("the run's message is missing %q:\n%s", want, finished.ErrorMessage)
		}
	}
	if len(states.created) != 0 {
		t.Errorf("a state version was filed for a run that produced no state: %v", states.created)
	}
}

// A plan produces no state and must not be reported as having lost one, or the
// message means nothing on the runs that did. It may still carry a notice about
// its diff, which is a different loss with a different sentence.
func TestRunJobReportsNoStateLossForANonMutatingRun(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "planstate")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "plan", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	w := newTestRunWorker(&recordingExecutor{})
	w.states = &stubStates{failLatest: pgx.ErrNoRows}
	w.storage = &stubBlobs{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "plan"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	for _, unwanted := range []string{"changed infrastructure", "state version", "predates this run"} {
		if strings.Contains(finished.ErrorMessage, unwanted) {
			t.Errorf("a plan was reported as having lost a state record — it says %q:\n%s", unwanted, finished.ErrorMessage)
		}
	}
}

// ── the partial state of a failed run ──────────────────────────────────────

// failingPartialExecutor is a failed apply that created some resources before
// stopping. The partial state it captured is the only record of which.
type failingPartialExecutor struct{ state []byte }

func (e *failingPartialExecutor) Execute(_ context.Context, params executor.ExecuteParams) (*executor.ExecuteResult, error) {
	params.LogCallback([]byte("creating...\n"))
	return &executor.ExecuteResult{Output: "partial", StateFile: e.state, StateJSON: e.state},
		errors.New("Error: creating EC2 instance: InsufficientInstanceCapacity")
}

// The run fails either way. What must not happen is the operator reading only
// "apply failed" while resources exist that nothing tracks.
func TestRunJobReportsBothHalvesWhenPartialStateIsLost(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "partial")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "apply", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	w := newTestRunWorker(&failingPartialExecutor{state: []byte(`{"version":4}`)})
	w.states = &stubStates{failLatest: pgx.ErrNoRows, failCreate: errors.New("duplicate key")}
	w.storage = &stubBlobs{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if finished.Status != "errored" {
		t.Errorf("status = %q, want errored", finished.Status)
	}
	// Half one: what tofu said. Half two: that the partial state was not kept.
	for _, want := range []string{"InsufficientInstanceCapacity", "partial state", "duplicate key", "nothing tracking them"} {
		if !strings.Contains(finished.ErrorMessage, want) {
			t.Errorf("the run's message is missing %q:\n%s", want, finished.ErrorMessage)
		}
	}
}

// The partial state landing must leave the run's own error alone, or every
// failed apply reads as two failures.
func TestRunJobKeepsTheExecutionErrorWhenPartialStateLands(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "partialok")

	wsID := id()
	exec(t, ctx,
		`INSERT INTO workspaces (id,org_id,name,created_by,source,tofu_version)
		 VALUES ($1,$2,$3,$4,'upload','1.11.0')`,
		wsID, orgID, "ws-"+wsID, userID)

	run, err := testQueries.CreateRun(ctx, repository.CreateRunParams{
		ID: id(), WorkspaceID: wsID, OrgID: orgID, Operation: "apply", Status: "pending", CreatedBy: userID,
		ConfigSource: "upload", ConfigTofuVersion: "1.11.0",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	states := &stubStates{failLatest: pgx.ErrNoRows}
	w := newTestRunWorker(&failingPartialExecutor{state: []byte(`{"version":4}`)})
	w.states = states
	w.storage = &stubBlobs{}

	if err := w.Work(ctx, &river.Job[RunJobArgs]{
		Args: RunJobArgs{RunID: run.ID, WorkspaceID: wsID, OrgID: orgID, Operation: "apply"},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	finished, err := testQueries.GetRun(ctx, repository.GetRunParams{ID: run.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !strings.Contains(finished.ErrorMessage, "InsufficientInstanceCapacity") {
		t.Errorf("the run lost what tofu said: %q", finished.ErrorMessage)
	}
	if strings.Contains(finished.ErrorMessage, "not recorded either") {
		t.Errorf("the partial state landed and the run claims it did not:\n%s", finished.ErrorMessage)
	}
	if len(states.created) != 1 || states.created[0].ResourceSummary != "partial (errored)" {
		t.Errorf("filed %v, want one row labelled partial", states.created)
	}
}

// The same separation on the state path: an apply on an instance whose
// configured storage is absent lost a state version it should have had, and the
// run has to say so. An instance that stores nothing by configuration does not
// annotate every apply with a line its operator cannot act on.
func TestRecordStateVersion_ReportsWhichAbsenceItHit(t *testing.T) {
	unconfigured := &RunJobWorker{states: &stubStates{}, storageIntent: StorageNotConfigured}
	if err := unconfigured.recordStateVersion(context.Background(), runArgs(), applied()); !errors.Is(err, errNoArtifactStorage) {
		t.Errorf("got %v, want errNoArtifactStorage", err)
	}

	broken := &RunJobWorker{states: &stubStates{}, storageIntent: StorageConfigured}
	err := broken.recordStateVersion(context.Background(), runArgs(), applied())
	if !errors.Is(err, errArtifactStorageUnavailable) {
		t.Errorf("got %v, want errArtifactStorageUnavailable", err)
	}
	if errors.Is(err, errNoArtifactStorage) {
		t.Error("a broken instance is exempt from the state-loss notice, so an apply that changed infrastructure records nothing and says nothing")
	}
}
