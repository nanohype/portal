package executor

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The secret a run pod would be leaking. Distinctive enough that finding it
// anywhere it does not belong is unambiguous.
const leaked = "ROTATE-ME-4a91c0d7-random_password.db.result"

// collect runs a stream through the demuxer and returns everything a subscriber
// would have seen — which is the union of what the callback was handed and what
// the visible output ended up holding, because the worker publishes the first
// and stores the second.
func collect(t *testing.T, stream string) (seen string, visible string, captured map[framed][]byte) {
	t.Helper()
	var emitted strings.Builder
	visible, captured, err := demuxFramed(strings.NewReader(stream), func(b []byte) {
		emitted.Write(b)
	})
	if err != nil {
		t.Fatalf("demuxFramed: %v", err)
	}
	return emitted.String(), visible, captured
}

// A framed payload must not reach the log callback.
//
// The callback is the worker's only fan-out point: it publishes to the run's
// WebSocket (ActionViewWorkspace) and accumulates into the buffer that becomes
// the S3 run-log object and, on a failed run, the runs.plan_output column served
// in the run detail JSON at that same bar. State carries generated passwords and
// provider credentials in cleartext, and its own download route is gated at
// ActionManageState — so a byte of it reaching this callback is disclosed a tier
// below the bar the download enforces, in three places, one of them persisted.
func TestFramedPayloadsNeverReachTheLogCallback(t *testing.T) {
	for _, f := range frames {
		t.Run(string(f.what), func(t *testing.T) {
			stream := strings.Join([]string{
				"Apply complete! Resources: 3 added, 0 changed, 0 destroyed.",
				f.begin,
				`{"outputs":{"pw":{"value":"` + leaked + `"}}}`,
				f.end,
				"Done.",
				"",
			}, "\n")

			seen, visible, captured := collect(t, stream)

			if strings.Contains(seen, leaked) {
				t.Errorf("the log callback was handed the framed payload: %q", seen)
			}
			if strings.Contains(visible, leaked) {
				t.Errorf("the framed payload survived into the visible output: %q", visible)
			}
			// The sentinels are portal's own plumbing; a user reading the run log
			// should not see them either.
			if strings.Contains(seen, f.begin) || strings.Contains(seen, f.end) {
				t.Errorf("a sentinel reached the log callback: %q", seen)
			}

			// ...and the payload still has to arrive, or the fix has broken the
			// State tab and the next run's restore instead of securing them.
			got := string(captured[f.what])
			if !strings.Contains(got, leaked) {
				t.Errorf("payload not captured for %s: %q", f.what, got)
			}

			// The ordinary output is untouched.
			for _, want := range []string{"Apply complete!", "Done."} {
				if !strings.Contains(seen, want) {
					t.Errorf("ordinary output %q did not reach the callback: %q", want, seen)
				}
			}
		})
	}
}

// Every sentinel the run script emits has to be one the reader suppresses.
//
// This is the gate that outlives the fix. It re-derives the sentinels from the
// rendered script instead of restating the frames table, so a payload added to
// buildScript with a hand-written `echo '===PORTAL_..._BEGIN==='` fails here
// rather than quietly streaming itself to every viewer.
func TestEveryFrameTheScriptEmitsIsSuppressed(t *testing.T) {
	suppressed := map[string]bool{}
	for _, f := range frames {
		suppressed[f.begin] = true
		suppressed[f.end] = true
	}

	// The one sentinel that is deliberately not a frame. It is a single line
	// carrying a resolved commit id — a value, not a payload, and not sensitive:
	// the run log names the branch it came from anyway.
	suppressed[commitMarker] = true

	e := &KubernetesExecutor{}
	sentinel := regexp.MustCompile(`===PORTAL_[A-Z_]*===`)

	found := map[string]bool{}
	for _, op := range []string{"plan", "apply", "destroy", "test"} {
		for _, src := range []string{"vcs", "upload"} {
			script := e.buildScript(ExecuteParams{Operation: op, Source: src})
			for _, m := range sentinel.FindAllString(script, -1) {
				found[m] = true
				if !suppressed[m] {
					t.Errorf("%s/%s emits %s, which no frame suppresses — its payload would stream to every log subscriber", op, src, m)
				}
			}
		}
	}

	// Guard against the assertion passing because it found nothing to check.
	if len(found) < 2*len(frames) {
		t.Errorf("found %d sentinels in the rendered scripts, expected at least %d — the extraction is not seeing the script", len(found), 2*len(frames))
	}
}

// A frame the pod never closed withholds everything after it.
//
// The pod died mid-payload, so the bytes still arriving ARE the payload. Passing
// them through on the grounds that the frame looked malformed is exactly the
// disclosure the frames exist to stop.
func TestAFrameThePodNeverClosedWithholdsTheRest(t *testing.T) {
	stream := strings.Join([]string{
		"Apply complete!",
		frameFor(framedStateFile).begin,
		`{"resources":[{"password":"` + leaked + `"}`,
		// pod is OOM-killed here — no closing sentinel, no further output
		"",
	}, "\n")

	seen, visible, captured := collect(t, stream)

	if strings.Contains(seen, leaked) {
		t.Errorf("an unterminated frame leaked to the callback: %q", seen)
	}
	if strings.Contains(visible, leaked) {
		t.Errorf("an unterminated frame leaked into the visible output: %q", visible)
	}
	// Half a state file is not state. Storing it would hand the next run a
	// corrupt file to restore from.
	if _, ok := captured[framedStateFile]; ok {
		t.Errorf("a truncated payload was captured as though it were complete: %q", captured[framedStateFile])
	}
	// Withheld, but not silently: a run log that stops early has to read as
	// withheld rather than as a run that stopped logging.
	if !strings.Contains(seen, "withheld") {
		t.Errorf("nothing told the reader why the log stops: %q", seen)
	}
}

// shellEcho models what `sh` writes to stdout for the single-quoted echo lines
// frame.open and frame.close render. `echo` terminates every line it prints,
// including an empty one — which is the whole reason the bare echo is there.
func shellEcho(t *testing.T, script string) string {
	t.Helper()
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		arg, ok := strings.CutPrefix(line, "echo ")
		if !ok {
			t.Fatalf("shellEcho cannot model %q — frame.open/close now render something other than echo", line)
		}
		arg = strings.TrimSuffix(strings.TrimPrefix(arg, "'"), "'")
		out.WriteString(arg + "\n")
	}
	return out.String()
}

// A payload whose last line has no trailing newline still frames correctly.
//
// `cat` of a file that does not end in one leaves the next thing printed glued
// to the payload's last line. If that next thing is the closing sentinel, a
// reader matching whole lines never sees it — and an unterminated frame withholds
// the rest of the run, so every apply on such a workspace loses its log from the
// state capture onward. The bare `echo` in frame.close is what prevents it.
//
// The stream is assembled from frame.open and frame.close rather than from the
// sentinels directly, so this fails if those two stop agreeing.
func TestAPayloadWithNoFinalNewlineStillCloses(t *testing.T) {
	f := frameFor(framedStateFile)

	// The payload deliberately does NOT end in a newline: this is `cat` of a
	// state file written without a trailing one.
	stream := "Apply complete!\n" +
		shellEcho(t, f.open()) +
		`{"pw":"` + leaked + `"}` +
		shellEcho(t, f.close()) +
		"Done.\n"

	// The property the bare echo exists for: the close supplies the newline the
	// payload did not, so the sentinel starts a line of its own either way.
	if !strings.HasPrefix(shellEcho(t, f.close()), "\n") {
		t.Errorf("frame.close does not open with a newline, so a payload with no final newline glues the sentinel to it: %q", f.close())
	}

	seen, visible, captured := collect(t, stream)

	if strings.Contains(seen, leaked) || strings.Contains(visible, leaked) {
		t.Errorf("payload leaked when it had no final newline: %q", seen)
	}
	if !strings.Contains(string(captured[framedStateFile]), leaked) {
		t.Errorf("payload not captured: %q", captured[framedStateFile])
	}
	if !strings.Contains(seen, "Done.") {
		t.Errorf("output after the frame was swallowed — the closing sentinel was glued to the payload: %q", seen)
	}
}

// A payload on one very long line is framed like any other.
//
// `show -json` writes an entire plan on a single line, and a large workspace
// pushes that past any fixed scan-token ceiling. A reader that gives up at a
// ceiling has one move left — hand the remainder over unframed — which is the
// leak arriving by a second route on exactly the workspaces with the most in
// their state.
func TestAnOversizedPayloadLineIsStillFramed(t *testing.T) {
	f := frameFor(framedPlanJSON)
	huge := `{"pad":"` + strings.Repeat("x", 4<<20) + `","pw":"` + leaked + `"}`

	stream := "Plan: 1 to add, 0 to change, 0 to destroy\n" +
		f.begin + "\n" + huge + "\n" + f.end + "\nDone.\n"

	seen, visible, captured := collect(t, stream)

	if strings.Contains(seen, leaked) {
		t.Errorf("an oversized payload line leaked to the callback")
	}
	if strings.Contains(visible, leaked) {
		t.Errorf("an oversized payload line leaked into the visible output")
	}
	if !strings.Contains(string(captured[framedPlanJSON]), leaked) {
		t.Errorf("oversized payload was not captured whole (got %d bytes)", len(captured[framedPlanJSON]))
	}
	if !strings.Contains(seen, "Done.") {
		t.Errorf("output after an oversized payload was swallowed: %q", seen)
	}
}

// A sentinel appearing INSIDE a payload is payload, not plumbing.
//
// State is user-influenced — a tenant can name a resource, or set a variable
// value, that contains portal's own sentinel text. Reading that as a frame
// boundary would let the contents of the state file steer the reader back into
// passthrough and stream the remainder of itself.
func TestASentinelInsideAPayloadDoesNotEndTheFrame(t *testing.T) {
	f := frameFor(framedStateFile)
	other := frameFor(framedPlanJSON)

	// The secret sits AFTER the embedded sentinel: a reader that treats the
	// embedded one as a boundary returns to passthrough and streams everything
	// from there on, which is the line that has to stay withheld.
	stream := strings.Join([]string{
		"Apply complete!",
		f.begin,
		`{"note":"` + other.begin + `",`,
		`{"note":"` + other.end + `",`,
		`"pw":"` + leaked + `"}`,
		f.end,
		"Done.",
		"",
	}, "\n")

	seen, visible, captured := collect(t, stream)

	if strings.Contains(seen, leaked) || strings.Contains(visible, leaked) {
		t.Errorf("a sentinel embedded in the payload broke the frame open: %q", seen)
	}
	// The whole payload, embedded sentinels and all, is what was captured.
	got := string(captured[framedStateFile])
	for _, want := range []string{other.begin, other.end, leaked} {
		if !strings.Contains(got, want) {
			t.Errorf("payload lost %q — part of it was routed somewhere else: %q", want, got)
		}
	}
	if !strings.Contains(seen, "Done.") {
		t.Errorf("the frame never closed on its own sentinel: %q", seen)
	}
}

// Ordinary output is passed through byte for byte.
//
// The framing must not become a filter on what users read: line endings, blank
// lines and ANSI colour all have to survive it, or every run log quietly changes
// shape.
func TestOrdinaryOutputPassesThroughUnchanged(t *testing.T) {
	lines := []string{
		"\033[1m$ tofu apply\033[0m",
		"",
		"aws_s3_bucket.logs: Creating...",
		"  # module.vpc.aws_subnet.private[0] will be created",
	}
	seen, visible, captured := collect(t, strings.Join(lines, "\n")+"\n")

	var want strings.Builder
	for _, l := range lines {
		want.WriteString(l + "\r\n")
	}
	if seen != want.String() {
		t.Errorf("passthrough changed the stream:\n got %q\nwant %q", seen, want.String())
	}
	if visible != strings.Join(lines, "\n")+"\n" {
		t.Errorf("visible output changed: %q", visible)
	}
	if len(captured) != 0 {
		t.Errorf("captured %d payloads from a stream with no frames", len(captured))
	}
}

// CRLF-terminated lines match sentinels the same as LF.
//
// A tofu build on a Windows-flavoured base image, or any tool writing CRLF,
// would otherwise never match a closing sentinel — and an unterminated frame
// withholds the rest of the run, turning a cosmetic difference into every apply
// losing its log.
func TestSentinelsMatchAcrossCRLF(t *testing.T) {
	f := frameFor(framedStateJSON)
	stream := "Apply complete!\r\n" + f.begin + "\r\n" + leaked + "\r\n" + f.end + "\r\nDone.\r\n"

	seen, _, captured := collect(t, stream)

	if strings.Contains(seen, leaked) {
		t.Errorf("CRLF payload leaked: %q", seen)
	}
	if !strings.Contains(string(captured[framedStateJSON]), leaked) {
		t.Errorf("CRLF payload not captured: %q", captured[framedStateJSON])
	}
	if !strings.Contains(seen, "Done.") {
		t.Errorf("CRLF closing sentinel did not match, so the rest was swallowed: %q", seen)
	}
}

// frameFor must have an entry for every declared payload, so a new framed
// constant cannot be referenced from buildScript before it has sentinels.
func TestEveryDeclaredPayloadHasAFrame(t *testing.T) {
	for _, what := range []framed{framedStateFile, framedStateJSON, framedPlanJSON} {
		f := frameFor(what)
		if f.begin == "" || f.end == "" || f.begin == f.end {
			t.Errorf("%s has a malformed frame: %+v", what, f)
		}
	}

	seen := map[string]framed{}
	for _, f := range frames {
		for _, s := range []string{f.begin, f.end} {
			if prev, dup := seen[s]; dup {
				t.Errorf("%s is the sentinel for both %s and %s", s, prev, f.what)
			}
			seen[s] = f.what
		}
	}
}

// A frame opening while another is still open is payload, not a new frame.
// Belt on the nesting rule: only the open frame's own closing sentinel closes it.
func TestOnlyTheOpenFramesOwnSentinelCloses(t *testing.T) {
	a, b := frameFor(framedStateFile), frameFor(framedPlanJSON)

	stream := fmt.Sprintf("start\n%s\nrow\n%s\n%s\nDone.\n", a.begin, b.end, a.end)
	seen, _, captured := collect(t, stream)

	if strings.Contains(seen, "row") {
		t.Errorf("another frame's closing sentinel released the payload: %q", seen)
	}
	if got := string(captured[framedStateFile]); !strings.Contains(got, b.end) {
		t.Errorf("the foreign sentinel should have been captured as payload, got %q", got)
	}
	if !strings.Contains(seen, "Done.") {
		t.Errorf("the frame did not close on its own sentinel: %q", seen)
	}
}
