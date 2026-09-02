package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// runFinishStore is the write that settles a run's terminal status. It is an
// interface because the corrective write below only happens when something else
// has already failed, and a concrete *repository.Queries puts that branch out of
// a test's reach.
type runFinishStore interface {
	UpdateRunFinished(ctx context.Context, arg repository.UpdateRunFinishedParams) (repository.Run, error)
}

// enqueueAutoApply inserts the apply job that a "queued" run promises, in the
// transaction that commits it.
func (w *RunJobWorker) enqueueAutoApply(ctx context.Context, args RunJobArgs) error {
	// A worker with no job-queue client cannot enqueue anything, and the run row
	// already says "queued". Treating that as "nothing to do" is the same
	// unkept promise as a failed insert, reached by configuration instead of by
	// failure.
	if w.riverClient == nil || w.db == nil {
		return errors.New("this worker has no job queue client, so no apply could be enqueued")
	}

	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the transaction that would carry the apply job: %w", err)
	}

	if _, err := w.riverClient.InsertTx(ctx, tx, RunJobArgs{
		RunID:       args.RunID,
		WorkspaceID: args.WorkspaceID,
		OrgID:       args.OrgID,
		Operation:   "apply",
	}, nil); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("insert the apply job: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the apply job: %w", err)
	}
	return nil
}

// withdrawAutoApplyPromise settles a run whose apply was never enqueued.
//
// The run row reads "queued", which every reading surface takes to mean an
// apply is coming. GetNextPendingRun claims status 'pending' only, so nothing
// picks such a run up, no later run of the workspace displaces it, and the
// pipeline or operator waiting on the apply waits on a job that does not exist.
// "planned" is what happened: the plan finished and no apply is queued. The
// notice carries the half a status cannot — that this workspace auto-applies, so
// an apply was expected and has to be started by hand.
//
// The return value is the invariant this holds: it is the status the run row
// carries when this returns, including when the corrective write fails and the
// row is still "queued".
//
// Everything downstream of the call keys on that status. The pipeline advancer
// decides what to do with the stage from it, and the completion log prints it. A
// caller that goes on using its own "queued" hands them a status the row does
// not hold — the advancer then takes its queued arm, which is a deliberate no-op
// resting on an apply being on its way, and leaves the stage running behind a
// run that will never move again.
func (w *RunJobWorker) withdrawAutoApplyPromise(ctx context.Context, args RunJobArgs, result *executor.ExecuteResult, cause error, logger *slog.Logger) string {
	notice := autoApplyNotice(cause)

	// The job context may already be cancelled; this write is what stops the run
	// claiming an apply that is not coming, so it gets a context of its own.
	writeCtx, cancel := durableContext(ctx)
	defer cancel()

	if _, err := w.finishes.UpdateRunFinished(writeCtx, repository.UpdateRunFinishedParams{
		ID:               args.RunID,
		Status:           "planned",
		PlanOutput:       &result.Output,
		ErrorMessage:     &notice,
		ResourcesAdded:   &result.ResourcesAdded,
		ResourcesChanged: &result.ResourcesChanged,
		ResourcesDeleted: &result.ResourcesDeleted,
	}); err != nil {
		logger.Error("the run still claims an apply that was never enqueued", "error", err,
			"consequence", "the run stays 'queued', nothing picks it up, and no later run of this workspace displaces it")
		return "queued"
	}

	w.streamer.Publish(args.RunID, []byte("\r\n\033[31m"+notice+"\033[0m\r\n"))
	return "planned"
}

// autoApplyNotice is what a run says when its plan succeeded and the apply that
// was supposed to follow was not enqueued.
func autoApplyNotice(cause error) string {
	return fmt.Sprintf(
		"the plan succeeded and this workspace applies automatically, but the apply was not enqueued: %s. "+
			"The run is recorded as planned rather than queued, because a queued run with no job behind it is picked up by nothing. "+
			"Start the apply by hand.",
		cause)
}
