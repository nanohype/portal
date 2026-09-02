package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/worker/executor"
)

// stateVersionStore is the read and the write that a run's state version needs.
// It is an interface for the reason pipelineAdvanceStore and runVariableStore
// are: the defects here live in what happens when one of these fails, and a
// concrete *repository.Queries puts those branches out of a test's reach.
type stateVersionStore interface {
	GetLatestStateVersion(ctx context.Context, arg repository.GetLatestStateVersionParams) (repository.StateVersion, error)
	CreateStateVersion(ctx context.Context, arg repository.CreateStateVersionParams) (repository.StateVersion, error)
}

// runBlobStore is everything the run path reads from and writes to object
// storage. RunJobWorker holds it as an interface rather than *storage.S3Storage
// so the failure of each object can be exercised.
//
// A nil *storage.S3Storage assigned into a field of this type is not a nil
// interface, and every `w.storage != nil` on the run path would then be true.
// NewRunJobWorker leaves the field unset instead — see the assignment there.
type runBlobStore interface {
	GetRawState(ctx context.Context, key string) ([]byte, error)
	GetState(ctx context.Context, key string) ([]byte, error)
	PutRawState(ctx context.Context, workspaceID string, serial int, data []byte) (string, error)
	PutState(ctx context.Context, workspaceID string, serial int, data []byte) (string, error)
	GetConfigArchive(ctx context.Context, key string) ([]byte, error)
	PutLog(ctx context.Context, runID, phase string, data []byte) (string, error)
	PutPlanJSON(ctx context.Context, runID string, data []byte) (string, error)
}

// rawStateKey is where the raw (possibly encrypted) state for a serial lives.
func rawStateKey(workspaceID string, serial int32) string {
	return fmt.Sprintf("state-raw/%s/%d.tfstate", workspaceID, serial)
}

// restorePreviousState returns the state this run continues from, and an error
// when the workspace has state that cannot be produced.
//
// Not having state and not being able to read it are different answers, and the
// run cannot tell them apart from the outside. A workspace at serial 47 whose
// state does not arrive plans against an empty state: `apply` then re-creates
// all 47 resources, or errors partway on the ones the provider refuses to
// duplicate. Neither outcome names a missing state — the run log is an ordinary
// log of a workspace being built, and the run row records success.
//
// So only pgx.ErrNoRows and serial 0 mean "no state yet". Everything else is a
// failure to answer the question, and the run stops before it executes, which
// is the one point on this path where stopping costs nothing.
func (w *RunJobWorker) restorePreviousState(ctx context.Context, args RunJobArgs, logger *slog.Logger) ([]byte, error) {
	latest, err := w.states.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
		WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up the workspace's latest state version: %w", err)
	}
	if latest.Serial <= 0 {
		return nil, nil
	}

	// A workspace only acquires state versions through a run that stored them,
	// so this is an instance that had object storage and lost it, never a
	// development instance that never had any.
	if w.storage == nil {
		return nil, fmt.Errorf("the workspace's state is recorded at serial %d and no object storage is configured, so it cannot be read", latest.Serial)
	}

	rawKey := rawStateKey(args.WorkspaceID, latest.Serial)
	rawState, rawErr := w.storage.GetRawState(ctx, rawKey)
	if rawErr == nil {
		logger.Info("fetched previous state (raw)", "serial", latest.Serial, "size", len(rawState))
		return rawState, nil
	}

	// Terragrunt workspaces never write a raw object — their state lives in
	// their own backend and portal records only what `state pull` returned — so
	// falling back to the browse object is the ordinary path there, not a
	// recovery.
	if latest.StateURL == "" {
		return nil, fmt.Errorf("state version %d has no browse object and %s could not be read: %w", latest.Serial, rawKey, rawErr)
	}
	browseState, err := w.storage.GetState(ctx, latest.StateURL)
	if err != nil {
		return nil, fmt.Errorf("state version %d could not be read, from %s (%v) or from %s: %w",
			latest.Serial, rawKey, rawErr, latest.StateURL, err)
	}
	logger.Info("fetched previous state (browse)", "serial", latest.Serial, "size", len(browseState))
	return browseState, nil
}

// stateOutcome is the state a run produced and how to label it.
type stateOutcome struct {
	StateFile       []byte
	StateJSON       []byte
	ResourceCount   int32
	ResourceSummary string
}

// producedState reports whether the executor captured any state at all.
func (o stateOutcome) producedState() bool {
	return o.StateFile != nil || o.StateJSON != nil
}

// recordStateVersion stores the state a run produced and files the version row
// that points at it, returning the reason it could not.
//
// The serial is never guessed. It comes from the latest recorded version, and a
// read that fails leaves it unknown — writing at serial 1 then overwrites the
// workspace's oldest state objects and files a row that sorts below the real
// latest, so the next run restores state from before this one and proposes
// re-creating everything this run built. Nothing about that is visible: the run
// row carries the correct resource counts and the State tab still shows the
// older serial.
//
// Every failure here is returned rather than logged, because the caller is the
// only place that knows the other half — whether infrastructure changed — and
// an operator needs both halves in one sentence.
func (w *RunJobWorker) recordStateVersion(ctx context.Context, args RunJobArgs, out stateOutcome) error {
	if w.storage == nil {
		return errors.New("no object storage is configured, so the state it produced was not stored")
	}

	latest, err := w.states.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
		WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("the next state serial could not be determined, so nothing was written rather than overwrite serial 1: %w", err)
	}
	nextSerial := latest.Serial + 1

	// The raw object preserves whatever encryption the backend applied and is
	// what the next run restores from. Terragrunt runs have no local state file
	// and skip it.
	if out.StateFile != nil {
		if _, err := w.storage.PutRawState(ctx, args.WorkspaceID, int(nextSerial), out.StateFile); err != nil {
			return fmt.Errorf("the raw state for serial %d could not be stored: %w", nextSerial, err)
		}
	}

	browseState := selectBrowseState(out.StateFile, out.StateJSON)
	if len(browseState) == 0 {
		return fmt.Errorf("neither a state file nor a state pull produced any content, so serial %d has nothing to point at", nextSerial)
	}

	stateURL, err := w.storage.PutState(ctx, args.WorkspaceID, int(nextSerial), browseState)
	if err != nil {
		return fmt.Errorf("the state for serial %d could not be stored: %w", nextSerial, err)
	}

	if _, err := w.states.CreateStateVersion(ctx, repository.CreateStateVersionParams{
		ID:              ulid.Make().String(),
		WorkspaceID:     args.WorkspaceID,
		OrgID:           args.OrgID,
		RunID:           args.RunID,
		Serial:          nextSerial,
		StateURL:        stateURL,
		ResourceCount:   out.ResourceCount,
		ResourceSummary: out.ResourceSummary,
	}); err != nil {
		return fmt.Errorf("serial %d was stored at %s but the state version row that points at it could not be written, so nothing can reach it: %w", nextSerial, stateURL, err)
	}
	return nil
}

// mutatesInfrastructure reports whether an operation changes what exists, which
// is what makes an unrecorded state a loss rather than a missing convenience.
// It is the same list startedStatusFor maps to "applying", minus "test", which
// runs a smoke script and produces no state of its own.
func mutatesInfrastructure(operation string) bool {
	switch operation {
	case "apply", "destroy", "import":
		return true
	default:
		return false
	}
}

// stateRecordFailure is what an operator is told when a run changed
// infrastructure and the record of that change did not land.
//
// Both halves have to be in it. "errored" alone denies that anything changed
// and sends the operator looking for a failed apply; the operational status
// alone hides that the workspace's recorded state no longer describes the
// infrastructure. The next run is the thing that goes wrong, so the message
// says what it will do.
func stateRecordFailure(operation, summary string, cause error) string {
	return fmt.Sprintf(
		"the %s succeeded and changed infrastructure (%s), but its state version was not recorded: %s. "+
			"The workspace's recorded state now predates this run, so the next run plans against it and may propose re-creating what this run built. "+
			"Reconcile the workspace's state before starting another run.",
		operation, summary, cause)
}

// resultStateFile and resultStateJSON read the captured state off a result that
// may be nil. A failed execution returns nil for some errors and a partial
// result for others, and the partial-state path has to handle both.
func resultStateFile(result *executor.ExecuteResult) []byte {
	if result == nil {
		return nil
	}
	return result.StateFile
}

func resultStateJSON(result *executor.ExecuteResult) []byte {
	if result == nil {
		return nil
	}
	return result.StateJSON
}
