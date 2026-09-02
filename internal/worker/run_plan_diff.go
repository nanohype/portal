package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nanohype/portal/internal/repository"
)

// planDiffStore is the write that points a run at its stored plan diff. It is an
// interface for the reason stateVersionStore is: the defect lives in what
// happens when the write fails, and a concrete *repository.Queries puts that
// branch out of a test's reach.
type planDiffStore interface {
	UpdateRunPlanJSONURL(ctx context.Context, arg repository.UpdateRunPlanJSONURLParams) error
}

// recordPlanDiff stores the machine-readable plan and points the run at it,
// returning the reason it could not.
//
// The diff is what an approval is granted against. `run.plan_json_url` empty is
// the only thing the reading surfaces check: the Changes tab is hidden when it
// is empty (web/src/components/run/RunView.tsx) and the plan-json endpoint
// refuses (internal/handler/run.go), so a run parked for approval with no diff
// asks an admin to sign off on changes nothing can show them. The plan text is
// still on the run, which is what makes the absence easy to miss — the run looks
// complete and only the tab is gone.
//
// Four things produce an empty plan_json_url and none of them announced itself:
// the executor generating no JSON plan, object storage being absent, the upload
// failing, and the row that points at the upload failing. The last leaves the
// diff in object storage with nothing referencing it.
func (w *RunJobWorker) recordPlanDiff(ctx context.Context, args RunJobArgs, planJSON []byte) error {
	if len(planJSON) == 0 {
		return errors.New("the executor produced no JSON plan, so this run has no machine-readable diff")
	}
	if w.storage == nil {
		return errNoArtifactStorage
	}

	planJSONURL, err := w.storage.PutPlanJSON(ctx, args.RunID, planJSON)
	if err != nil {
		return fmt.Errorf("the plan diff could not be stored: %w", err)
	}

	if err := w.plans.UpdateRunPlanJSONURL(ctx, repository.UpdateRunPlanJSONURLParams{
		ID: args.RunID, PlanJSONURL: planJSONURL,
	}); err != nil {
		return fmt.Errorf("the plan diff was stored at %s but the run could not be pointed at it, so nothing can reach it: %w", planJSONURL, err)
	}
	return nil
}

// missingDiffBlocksApproval reports whether a run that has no plan diff must
// fail rather than park.
//
// An instance with no object storage is excluded, because it stores no artefact
// for any run: the plan-json endpoint answers 503 "storage not configured" there
// whatever the run did (internal/handler/run.go). Refusing on that would make
// every approval-gated workspace unusable on an instance that has chosen not to
// persist artefacts, which is a supported configuration rather than a fault.
//
// "awaiting_approval" is the one status where the diff is not a convenience: it
// is the artefact the approval is about, and the approval is what authorises an
// apply against this row. The other statuses lose a view — "planned" loses a tab
// on a run nobody is signing, and "queued" applies without a human reading
// anything — so they are reported on the run instead of failing it.
//
// Only a plan produces a JSON plan at all; the executors generate it for that
// operation and no other. Reporting a missing diff on an apply would be a false
// alarm on every apply there is.
func missingDiffBlocksApproval(operation, finalStatus string, cause error) bool {
	return operation == "plan" && finalStatus == "awaiting_approval" && !errors.Is(cause, errNoArtifactStorage)
}

// reportsMissingDiff reports whether the absence of a diff is worth saying on a
// run that is not being failed for it. An instance that stores no artefacts is
// excluded for the reason errNoArtifactStorage gives.
func reportsMissingDiff(operation string, cause error) bool {
	return operation == "plan" && !errors.Is(cause, errNoArtifactStorage)
}

// missingDiffNotice is what a run that kept its status says about the diff it
// does not have.
func missingDiffNotice(cause error) string {
	return fmt.Sprintf(
		"the plan succeeded and its output is on this run, but no machine-readable diff was recorded: %s. "+
			"The Changes tab has nothing to show; read the plan output instead.",
		cause)
}

// joinRunNotices collects what a finished run has to tell an operator into the
// one field that reaches them, or nil when there is nothing to say.
//
// A run can lose more than one thing — an apply whose state did not record and
// whose diff did not either — and dropping all but the first would make the
// surface depend on the order the failures happened in.
func joinRunNotices(notices []string) *string {
	if len(notices) == 0 {
		return nil
	}
	joined := strings.Join(notices, " ")
	return &joined
}

// errNoArtifactStorage is the worker having no object storage at all.
//
// It separates "this instance stores no artefacts" from "this run failed to
// store one", which the run path could not tell apart while both arrived as a
// nil store. The first is a configuration portal supports — every reading
// endpoint answers 503 "storage not configured" on such an instance — and
// reporting it on every run would put a line an operator cannot act on onto
// every run they open, which is how a field stops being read.
//
// What it cannot distinguish is object storage that was configured and was
// unreachable when the worker started: cmd/worker/main.go leaves the store nil
// in that case too, deliberately, so a crashloop does not replace a degraded
// start. On such an instance this reads as the declared shape.
var errNoArtifactStorage = errors.New("the worker has no object storage configured, so nothing was stored")
