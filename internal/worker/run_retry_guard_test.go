package worker

import "testing"

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
