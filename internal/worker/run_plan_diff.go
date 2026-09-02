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
// Four things leave plan_json_url empty and each has to reach the caller: the
// executor generating no JSON plan, object storage being absent, the upload
// failing, and the row that points at the upload failing. The last is the one
// worth naming in its own message — the diff is in object storage with nothing
// referencing it, so the recovery differs from a failed upload.
func (w *RunJobWorker) recordPlanDiff(ctx context.Context, args RunJobArgs, planJSON []byte) error {
	if len(planJSON) == 0 {
		return errors.New("the executor produced no JSON plan, so this run has no machine-readable diff")
	}
	if w.storage == nil {
		return w.absentStorageError()
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
// One run produces at most one notice today: reportsMissingDiff answers only for
// "plan" and mutatesInfrastructure only for apply, destroy and import, and
// TestRunNotices_ProducersAreDisjointByOperation holds that apart. The single
// field is what makes joining rather than assigning the right shape anyway — a
// third producer added to it must not silently replace the first, and which one
// survived would otherwise depend on the order the checks run in.
func joinRunNotices(notices []string) *string {
	if len(notices) == 0 {
		return nil
	}
	joined := strings.Join(notices, " ")
	return &joined
}

// errNoArtifactStorage is an instance that is not configured to store run
// artefacts at all.
//
// It marks the one absence that is a declared shape rather than a fault. Portal
// supports running with no object storage, and says so where the diff is read:
// the plan-json endpoint answers 503 "storage not configured"
// (internal/handler/run.go). No run on such an instance can carry a diff, so
// refusing or annotating each one would put a line on every run that its
// operator cannot act on.
//
// It is deliberately not returned when object storage is configured and absent.
// cmd/worker/main.go leaves the store nil on that arm too — on purpose, so an
// unreachable S3 degrades rather than crashloops — and the two arms mean
// opposite things to a run: one instance never had a diff to lose, the other
// lost it. StorageIntent is what tells them apart.
var errNoArtifactStorage = errors.New("this instance is not configured to store run artifacts, so nothing was stored")

// errArtifactStorageUnavailable is object storage that this instance is
// configured for and that the worker does not have. It is a fault, and it is
// not exempt from anything.
var errArtifactStorageUnavailable = errors.New("this instance is configured to store run artifacts and the worker has no object storage, so nothing was stored")

// StorageIntent says whether an instance is configured to store run artifacts.
//
// It is not "is a store available right now". cmd/worker/main.go leaves the
// store nil both when none is configured and when a configured one fails its
// bounded boot check, so the nil alone cannot separate an instance that never
// had artifacts to lose from one that lost them.
type StorageIntent bool

const (
	// StorageNotConfigured is an instance with no S3 endpoint set.
	StorageNotConfigured StorageIntent = false
	// StorageConfigured is an instance with one set, whether or not the store
	// reached the worker.
	StorageConfigured StorageIntent = true
)

// StorageIntentFor reads the intent off the setting that decides it. One rule,
// so the worker and the approval gate cannot disagree about which instance they
// are running on.
func StorageIntentFor(s3Endpoint string) StorageIntent {
	return StorageIntent(s3Endpoint != "")
}

// absentStorageError is the error a missing store produces, which depends on
// whether this instance meant to have one.
func (w *RunJobWorker) absentStorageError() error {
	if w.storageIntent == StorageConfigured {
		return errArtifactStorageUnavailable
	}
	return errNoArtifactStorage
}
