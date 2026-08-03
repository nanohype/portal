package executor

import (
	"strings"
	"testing"
)

// Keeping a payload out of the log is only half the job: it still has to arrive
// where the worker reads it back. These cover the wiring between the two, which
// is where the fix could half-apply — the run log clean, the State tab blank,
// and the next run restoring nothing.

// Each frame lands on the ExecuteResult field that reads it.
//
// A nil or swapped mapping is invisible from the run log, so nothing about a run
// looks wrong. It shows up later: a State tab that stopped populating, a plan
// diff that went blank, a next run with no prior state to restore from.
func TestEachFrameLandsOnTheResultFieldThatReadsIt(t *testing.T) {
	result := resultFromCaptured("visible output", map[framed][]byte{
		framedStateFile: []byte("RAW-STATE"),
		framedStateJSON: []byte("DECRYPTED-STATE"),
		framedPlanJSON:  []byte("PLAN-JSON"),
	})

	for _, tc := range []struct {
		field string
		got   []byte
		want  string
	}{
		{"StateFile", result.StateFile, "RAW-STATE"},
		{"StateJSON", result.StateJSON, "DECRYPTED-STATE"},
		{"PlanJSON", result.PlanJSON, "PLAN-JSON"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if result.Output != "visible output" {
		t.Errorf("Output = %q, want the visible output", result.Output)
	}
}

// A run whose pod sent no frames leaves those fields nil rather than empty, so
// the worker's `result.StateFile != nil` checks still tell "no state" apart from
// "state that happens to be empty" — the difference between skipping the state
// upload and writing an empty state version over a good one.
func TestNoFramesLeavesTheResultFieldsNil(t *testing.T) {
	result := resultFromCaptured("just logs", map[framed][]byte{})

	if result.StateFile != nil || result.StateJSON != nil || result.PlanJSON != nil {
		t.Errorf("expected nil payload fields, got %+v", result)
	}
}

// An apply's full stream ends with the payloads captured and the log clean.
//
// End to end over the demuxer with a stream shaped like a real apply — ordinary
// output, then both state frames back to back — because the per-frame tests each
// see only one frame and would not catch the second one being dropped.
func TestAnApplyStreamCapturesBothStatePayloadsAndLogsNeither(t *testing.T) {
	stream := strings.Join([]string{
		"\033[1m$ tofu apply\033[0m",
		"aws_db_instance.main: Creating...",
		"Apply complete! Resources: 1 added, 0 changed, 0 destroyed.",
		frameFor(framedStateFile).begin,
		`{"serial":7,"pw":"` + leaked + `"}`,
		frameFor(framedStateFile).end,
		frameFor(framedStateJSON).begin,
		`{"serial":7,"decrypted":"` + leaked + `"}`,
		frameFor(framedStateJSON).end,
		"",
	}, "\n")

	seen, visible, captured := collect(t, stream)
	result := resultFromCaptured(visible, captured)

	if strings.Contains(seen, leaked) {
		t.Errorf("state reached the log callback: %q", seen)
	}
	if strings.Contains(result.Output, leaked) {
		t.Errorf("state reached the stored run log: %q", result.Output)
	}
	if !strings.Contains(seen, "Apply complete!") {
		t.Errorf("ordinary output did not reach the callback: %q", seen)
	}

	if !strings.Contains(string(result.StateFile), leaked) {
		t.Errorf("the raw state never landed on StateFile: %q", result.StateFile)
	}
	if !strings.Contains(string(result.StateJSON), "decrypted") {
		t.Errorf("the decrypted state never landed on StateJSON: %q", result.StateJSON)
	}
	if result.PlanJSON != nil {
		t.Errorf("an apply produced a PlanJSON: %q", result.PlanJSON)
	}
}
