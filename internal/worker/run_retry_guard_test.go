package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/repository"
)

// The failure this guards: UpdateRunFinished fails after tofu has already run,
// River retries the job from the top, and nothing stops a second destroy. The
// run row still reads "applying" precisely because the write that would have
// moved it off "applying" is the one that failed.
func TestRefusesRetry(t *testing.T) {
	cases := []struct {
		name    string
		attempt int
		status  string
		want    bool
		why     string
	}{
		{
			name: "first attempt always proceeds", attempt: 1, status: "applying", want: false,
			why: "attempt 1 is the run itself, not a retry",
		},
		{
			name: "retry after a mutating operation reached execution", attempt: 2, status: "applying", want: true,
			why: "a previous attempt may have already changed infrastructure",
		},
		{
			name: "later retries keep refusing", attempt: 5, status: "applying", want: true,
			why: "MaxAttempts is 5; every one of them must refuse, not just the second",
		},
		{
			name: "retry of a plan is safe", attempt: 2, status: "planning", want: false,
			why: "a plan writes nothing, so re-running it costs time and nothing else",
		},
		{
			name: "retry before UpdateRunStarted landed is safe", attempt: 2, status: "pending", want: false,
			why: "the first attempt failed on its first write, so tofu never ran",
		},
		{
			name: "queued is the auto-apply handoff, not an executed run", attempt: 2, status: "queued", want: false,
			why: "a plan that finishes with auto-apply leaves the run queued for the apply job",
		},
		{
			name: "a run that already finished is not re-executed by this path", attempt: 2, status: "applied", want: false,
			why: "terminal statuses are unreachable as a prior status here; proceeding is harmless and the guard is not what stops it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusesRetry(tc.attempt, tc.status); got != tc.want {
				t.Errorf("refusesRetry(%d, %q) = %v, want %v — %s", tc.attempt, tc.status, got, tc.want, tc.why)
			}
		})
	}
}

// The guard keys on the status string this worker writes for mutating
// operations. Calling the real mapping rather than restating it is the point: a
// test that reimplements the code under test proves only that the
// reimplementation works, and this coupling is exactly what would rot silently.
func TestApplyingIsTheStatusForMutatingOperations(t *testing.T) {
	for _, op := range []string{"apply", "destroy", "import", "test"} {
		status := startedStatusFor(op)
		if status != "applying" {
			t.Errorf("startedStatusFor(%q) = %q; the retry guard only refuses on \"applying\"", op, status)
		}
		if !refusesRetry(2, status) {
			t.Errorf("a retried %q must be refused", op)
		}
	}

	for _, op := range []string{"plan", ""} {
		status := startedStatusFor(op)
		if status != "planning" {
			t.Errorf("startedStatusFor(%q) = %q, want planning", op, status)
		}
		if refusesRetry(2, status) {
			t.Errorf("a retried %q must not be refused — it writes nothing", op)
		}
	}
}

// river.Job embeds *rivertype.JobRow — a pointer — so reading job.Attempt on a
// Job built from Args alone dereferences nil and panics. Every test in this
// package constructs jobs that way, and so the retry guard took down an
// unrelated test the first time it shipped. Production always populates JobRow;
// this is about not assuming it.
func TestAttemptOf(t *testing.T) {
	t.Run("a job with no JobRow reads as the first attempt", func(t *testing.T) {
		job := &river.Job[RunJobArgs]{Args: RunJobArgs{RunID: "r1"}}
		if got := attemptOf(job); got != 1 {
			t.Errorf("attemptOf = %d, want 1 when JobRow is absent", got)
		}
		// The consequence that matters: no attempt information must not be
		// mistaken for a retry, or a first run would refuse itself.
		if refusesRetry(attemptOf(job), "applying") {
			t.Error("a first attempt must never be refused")
		}
	})

	t.Run("a nil job does not panic", func(t *testing.T) {
		if got := attemptOf[RunJobArgs](nil); got != 1 {
			t.Errorf("attemptOf(nil) = %d, want 1", got)
		}
	})
}

// The guard is only as good as the read that arms it. GetRun failing on a retry
// leaves the prior status unknown — and the database that fails that read is the
// same one whose failed write left the run at "applying", so the two arrive
// together. Reading the failure as "nothing to refuse" armed the guard precisely
// in the cases where nothing had gone wrong.
func TestRetryRefusal_AnUnreadableStatusStillRefusesAMutatingOperation(t *testing.T) {
	for _, op := range []string{"apply", "destroy", "import", "test"} {
		if !retryRefusal(2, "", false, op) {
			t.Errorf("a retried %q whose prior status could not be read was allowed to execute; a previous attempt having already changed infrastructure cannot be ruled out", op)
		}
	}

	// A plan writes nothing, so an unreadable status costs it nothing to
	// proceed. Refusing here would fail runs for a database blip and teach
	// operators to retry past the guard.
	for _, op := range []string{"plan", ""} {
		if retryRefusal(2, "", false, op) {
			t.Errorf("a retried %q was refused; a plan writes nothing, so an unreadable status is not a reason to fail it", op)
		}
	}

	// The unreadable case must not leak into the first attempt, which is the
	// run itself.
	for _, op := range []string{"apply", "destroy", "plan"} {
		if retryRefusal(1, "", false, op) {
			t.Errorf("a first attempt at %q was refused", op)
		}
	}
}

// A status that was read decides on its own. Deriving the answer from the
// operation when the status is available would put two mappings in play and let
// them disagree.
func TestRetryRefusal_AReadableStatusDecidesOnItsOwn(t *testing.T) {
	cases := []struct {
		attempt int
		status  string
		op      string
		want    bool
		why     string
	}{
		{2, "applying", "apply", true, "the recorded status says a previous attempt reached execution"},
		{2, "planning", "apply", false, "a plan writes nothing, whatever operation this job carries"},
		{2, "pending", "destroy", false, "the first attempt never got past UpdateRunStarted"},
		{2, "queued", "apply", false, "queued is the auto-apply handoff, not an executed run"},
		{1, "applying", "destroy", false, "attempt 1 is the run itself"},
	}
	for _, tc := range cases {
		if got := retryRefusal(tc.attempt, tc.status, true, tc.op); got != tc.want {
			t.Errorf("retryRefusal(%d, %q, read, %q) = %v, want %v — %s", tc.attempt, tc.status, tc.op, got, tc.want, tc.why)
		}
	}
}

// The two paths must agree wherever both have an answer, or which one ran would
// change the outcome.
func TestRetryRefusal_AgreesWithRefusesRetryWhenTheStatusIsRead(t *testing.T) {
	for _, status := range []string{"applying", "planning", "pending", "queued", "applied", "errored", ""} {
		for _, attempt := range []int{1, 2, 5} {
			want := refusesRetry(attempt, status)
			if got := retryRefusal(attempt, status, true, "apply"); got != want {
				t.Errorf("retryRefusal(%d, %q, read, apply) = %v, refusesRetry = %v", attempt, status, got, want)
			}
		}
	}
}

// The pure decision is one half. The other half is the read that feeds it, and
// that is where the guard was disarmed: an error from GetRun was folded into the
// arming condition, so the guard was armed only when the read succeeded. These
// tests drive the method that performs the read.

type stubRuns struct {
	run   repository.Run
	fail  error
	reads int
}

func (s *stubRuns) GetRun(context.Context, repository.GetRunParams) (repository.Run, error) {
	s.reads++
	return s.run, s.fail
}

func refusalFor(t *testing.T, st *stubRuns, attempt int, operation string) (string, string) {
	t.Helper()
	var buf strings.Builder
	w := &RunJobWorker{runs: st}
	reason := w.retryRefusalReason(context.Background(), attempt, RunJobArgs{
		RunID: "run_1", WorkspaceID: "ws_1", OrgID: "org_1", Operation: operation,
	}, capture(&buf))
	return reason, buf.String()
}

func TestRetryRefusalReason_RefusesAMutatingRetryWhenTheStatusReadFails(t *testing.T) {
	for _, op := range []string{"apply", "destroy", "import", "test"} {
		st := &stubRuns{fail: errors.New("connection reset by peer")}
		reason, logged := refusalFor(t, st, 2, op)
		if reason == "" {
			t.Errorf("a retried %q proceeded although the status of the previous attempt could not be read; that attempt may have already changed infrastructure", op)
		}
		if !strings.Contains(reason, "could not be read") {
			t.Errorf("the refusal for %q does not say the status was unreadable, so an operator cannot tell this from a recorded 'applying': %q", op, reason)
		}
		if !strings.Contains(logged, "connection reset by peer") {
			t.Errorf("the read failure for %q is not in the log, so the cause is unavailable:\n%s", op, logged)
		}
	}
}

func TestRetryRefusalReason_LetsAPlanRetryProceedWhenTheStatusReadFails(t *testing.T) {
	st := &stubRuns{fail: errors.New("connection reset by peer")}
	if reason, _ := refusalFor(t, st, 2, "plan"); reason != "" {
		t.Errorf("a retried plan was refused for an unreadable status: %q; a plan writes nothing", reason)
	}
}

func TestRetryRefusalReason_RefusesOnARecordedApplyingStatus(t *testing.T) {
	st := &stubRuns{run: repository.Run{Status: "applying"}}
	reason, _ := refusalFor(t, st, 2, "apply")
	if reason == "" {
		t.Fatal("a retry of a run recorded as applying was allowed to execute")
	}
	if strings.Contains(reason, "could not be read") {
		t.Errorf("the status was read; the refusal must say so rather than claim it was unavailable: %q", reason)
	}
}

func TestRetryRefusalReason_ProceedsOnTheFirstAttemptWithoutReading(t *testing.T) {
	// The first attempt is the run itself, and every run is a first attempt.
	// Reading here would put a query and — while the database is degraded — a
	// warning line on the ordinary path, for an answer that cannot change.
	st := &stubRuns{fail: errors.New("connection reset by peer")}
	reason, logged := refusalFor(t, st, 1, "destroy")
	if reason != "" {
		t.Errorf("the first attempt at a destroy was refused: %q", reason)
	}
	if st.reads != 0 {
		t.Errorf("the first attempt read the previous attempt's status %d times; there is no previous attempt", st.reads)
	}
	if logged != "" {
		t.Errorf("the first attempt logged about a previous attempt:\n%s", logged)
	}
}

func TestRetryRefusalReason_ProceedsWhenTheRecordedStatusSaysNothingRan(t *testing.T) {
	for _, status := range []string{"pending", "queued", "planning"} {
		st := &stubRuns{run: repository.Run{Status: status}}
		if reason, _ := refusalFor(t, st, 2, "apply"); reason != "" {
			t.Errorf("a retry with recorded status %q was refused: %q; nothing executed", status, reason)
		}
	}
}
