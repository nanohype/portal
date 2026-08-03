package executor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A run pod has exactly one channel back to the worker: its log stream. The raw
// state file, the decrypted state and the JSON plan all have to travel on it —
// the pod's work directory is an emptyDir the worker cannot read, and reaching
// in for it would mean holding pods/exec on the executor namespace.
//
// So each payload is FRAMED, and the frames are demultiplexed at the point of
// arrival: a framed span goes to its own buffer and never reaches the log
// callback. That callback is the only fan-out point the worker has — everything
// downstream is a copy of whatever it was handed:
//
//	logCallback ─┬─ streamer.Publish ── the run's WebSocket, ActionViewWorkspace
//	             └─ logBuf ─┬─ storage.PutLog ─────── the S3 run-log object
//	                        └─ failRun(plan_output) ─ a Postgres column served in
//	                                                  the run detail JSON, also
//	                                                  ActionViewWorkspace
//
// tofu writes random_password.result, tls_private_key.private_key_pem and
// provider credentials into state in cleartext, which is why the state download
// route is gated at ActionManageState. Anything reaching the callback has
// therefore been disclosed a tier below the bar its own download route enforces,
// in three places at once, one of them persisted.
//
// Which is why the demultiplexing happens HERE rather than after the pod exits:
// by then the callback has already published every line.
//
// The local executor needs none of this. It runs `state pull` and `show -json`
// as separate commands and reads their output directly, so the material never
// enters its log stream at all (local.go). Framing is the Kubernetes executor's
// substitute for that separation, and it has to hold the same line.

// framed names a payload the run script sends back inside a frame. The value is
// the phrase used when a frame has to be reported as withheld, so it reads as a
// sentence in the run log.
type framed string

const (
	framedStateFile framed = "the state file"
	framedStateJSON framed = "the decrypted state"
	framedPlanJSON  framed = "the JSON plan"
)

// frame is one payload's sentinel pair.
//
// buildScript emits these and demuxFramed suppresses them, and both read this
// one table — so a payload cannot be added to the run script without also being
// suppressed. That is the property TestEveryFrameTheScriptEmitsIsSuppressed
// holds: it re-derives the sentinels from the rendered script rather than
// restating this table, so a hand-written `echo '===PORTAL_..._BEGIN==='` added
// to buildScript fails the build instead of quietly streaming its payload.
type frame struct {
	what  framed
	begin string
	end   string
}

var frames = []frame{
	{framedStateFile, "===PORTAL_STATE_BEGIN===", "===PORTAL_STATE_END==="},
	{framedStateJSON, "===PORTAL_STATE_JSON_BEGIN===", "===PORTAL_STATE_JSON_END==="},
	{framedPlanJSON, "===PORTAL_PLAN_JSON_BEGIN===", "===PORTAL_PLAN_JSON_END==="},
}

func frameFor(what framed) frame {
	for _, f := range frames {
		if f.what == what {
			return f
		}
	}
	panic("executor: no frame declared for " + string(what))
}

// open renders the shell that starts the frame.
func (f frame) open() string { return "echo '" + f.begin + "'\n" }

// close renders the shell that ends it.
//
// The bare `echo` is load-bearing. `cat` of a file whose last line has no
// newline would otherwise leave the closing sentinel glued to the end of the
// payload, where a reader matching whole lines never sees it — and a frame that
// never closes withholds the rest of the run's output. The blank line it adds is
// trimmed off the captured payload.
func (f frame) close() string { return "echo ''\necho '" + f.end + "'\n" }

// demuxFramed reads a run pod's log stream, hands the ordinary lines to emit,
// and routes each framed payload into its own buffer. Nothing inside a frame —
// including the sentinels themselves — reaches emit or the visible output.
//
// It reads with a bufio.Reader rather than a bufio.Scanner because a scanner has
// to be given a maximum token size, and `show -json` writes a whole plan on one
// line: past that ceiling a scanner stops mid-stream, and the only thing left to
// do with the remainder is hand it over unframed.
func demuxFramed(r io.Reader, emit func([]byte)) (string, map[framed][]byte, error) {
	d := &demuxer{emit: emit, captured: map[framed][]byte{}}

	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadString('\n')
		if raw != "" {
			d.line(strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r"))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				d.finish()
				return d.visible.String(), d.captured, err
			}
			break
		}
	}

	d.finish()
	return d.visible.String(), d.captured, nil
}

// resultFromCaptured places each demultiplexed payload on the field that reads
// it back: StateFile is restored onto the next run, StateJSON backs the State
// tab, PlanJSON backs the plan diff.
//
// Its own function because a payload that is framed but never landed is
// invisible from the run log — the log is clean either way — and surfaces only
// later, as a State tab that stopped populating and a next run with nothing to
// restore from.
func resultFromCaptured(output string, captured map[framed][]byte) *ExecuteResult {
	return &ExecuteResult{
		Output:    output,
		StateFile: captured[framedStateFile],
		StateJSON: captured[framedStateJSON],
		PlanJSON:  captured[framedPlanJSON],
	}
}

type demuxer struct {
	emit     func([]byte)
	visible  strings.Builder
	captured map[framed][]byte
	open     *frame
	payload  bytes.Buffer
}

func (d *demuxer) line(line string) {
	// Inside a frame everything is payload until the exact closing sentinel,
	// including a line that looks like another frame's opener. Treating a
	// nested opener as data is the safe reading: the alternative lets a value
	// inside the state file steer the reader back into passthrough.
	if d.open != nil {
		if line == d.open.end {
			d.captured[d.open.what] = append([]byte(nil), bytes.TrimSpace(d.payload.Bytes())...)
			d.payload.Reset()
			d.open = nil
			return
		}
		d.payload.WriteString(line)
		d.payload.WriteByte('\n')
		return
	}

	for i := range frames {
		if frames[i].begin == line {
			d.open = &frames[i]
			return
		}
	}

	d.visible.WriteString(line)
	d.visible.WriteString("\n")
	d.emit([]byte(line + "\r\n"))
}

// finish reports a frame the pod never closed.
//
// Everything that arrived after the opening sentinel has already been withheld,
// which is the direction to fail in: the pod died mid-payload and the bytes
// still arriving are the payload. What it accumulated is dropped rather than
// captured — half a state file is not state, and keeping it would hand the next
// run a corrupt one to restore from.
//
// Announced rather than silent, so a run log that stops early reads as withheld
// and not as a run that stopped logging.
func (d *demuxer) finish() {
	if d.open == nil {
		return
	}

	notice := fmt.Sprintf("[portal] %s was withheld — the pod ended before it finished sending it.", d.open.what)
	d.visible.WriteString(notice)
	d.visible.WriteString("\n")
	d.emit([]byte(notice + "\r\n"))

	d.open = nil
	d.payload.Reset()
}
