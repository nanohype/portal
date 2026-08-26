package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/nanohype/portal/internal/logstream"
	"github.com/nanohype/portal/internal/metrics"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/varmerge"
	"github.com/nanohype/portal/internal/worker/executor"
)

type ImportResource struct {
	Address string `json:"address"`
	ID      string `json:"id"`
}

type RunJobArgs struct {
	RunID             string           `json:"run_id"`
	WorkspaceID       string           `json:"workspace_id"`
	OrgID             string           `json:"org_id"`
	Operation         string           `json:"operation"`
	Imports           []ImportResource `json:"imports,omitempty"`
	AutoApplyOverride *bool            `json:"auto_apply_override,omitempty"`
}

func (RunJobArgs) Kind() string { return "run" }

func (RunJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:    "default",
		Priority: 1,
		// Bound retries: a run whose DB writes keep failing should fail visibly
		// after a few attempts, not back off silently for hours toward River's
		// default of 25. The job itself already turns tofu errors into a terminal
		// run status (no retry); these attempts are for infrastructure failures.
		MaxAttempts: 5,
	}
}

// pipelineAdvanceStore is the slice of the query layer that advancePipelineIfNeeded
// touches. It exists as an interface so a test can make each of these writes fail
// and watch what the advancement does about it — every one of them is a state
// transition whose failure leaves a pipeline in a state nothing sweeps, and a
// concrete *repository.Queries puts those branches out of reach.
type pipelineAdvanceStore interface {
	GetPipelineRunStageByRunID(ctx context.Context, runID string) (repository.PipelineRunStage, error)
	GetPipelineRun(ctx context.Context, arg repository.GetPipelineRunParams) (repository.PipelineRun, error)
	FinishPipelineRunStage(ctx context.Context, id, status string) (repository.PipelineRunStage, error)
	FinishPipelineRun(ctx context.Context, id, status string) (repository.PipelineRun, error)
	CancelPendingPipelineRunStages(ctx context.Context, pipelineRunID string) error
	UpdatePipelineRunStageStatus(ctx context.Context, arg repository.UpdatePipelineRunStageStatusParams) (repository.PipelineRunStage, error)
}

type RunJobWorker struct {
	river.WorkerDefaults[RunJobArgs]
	queries     *repository.Queries
	pipelines   pipelineAdvanceStore
	executor    executor.Executor
	streamer    logstream.Streamer
	storage     *storage.S3Storage // nil in dev without MinIO
	encryptor   *secrets.Encryptor // nil if encryption not configured
	riverClient *river.Client[pgx.Tx]
	db          *pgxpool.Pool
}

// Timeout returns the maximum duration a run job can execute before River cancels it.
func (w *RunJobWorker) Timeout(*river.Job[RunJobArgs]) time.Duration {
	return 2 * time.Hour
}

func NewRunJobWorker(queries *repository.Queries, exec executor.Executor, streamer logstream.Streamer, store *storage.S3Storage, encryptor *secrets.Encryptor) *RunJobWorker {
	return &RunJobWorker{
		pipelines: queries,
		queries:   queries,
		executor:  exec,
		streamer:  streamer,
		storage:   store,
		encryptor: encryptor,
	}
}

func (w *RunJobWorker) SetRiverClient(client *river.Client[pgx.Tx], db *pgxpool.Pool) {
	w.riverClient = client
	w.db = db
}

// startedStatusFor is the status a run carries while this worker is executing it.
// The mutating operations share "applying", which is what lets refusesRetry key
// on the status alone instead of a second list of operations that could drift
// from this one.
func startedStatusFor(operation string) string {
	switch operation {
	case "apply", "destroy", "import", "test":
		return "applying"
	default:
		return "planning"
	}
}

// attemptOf reads a job's attempt number without assuming it has one.
//
// river.Job embeds *rivertype.JobRow, a pointer, so job.Attempt dereferences it.
// River always populates it in production, but a caller constructing a Job from
// Args alone — which every test in this package does — leaves it nil, and
// reading Attempt panics. Returning 1 for that case is the honest default: no
// attempt information means treat this as the first attempt, which is how the
// worker behaved before the retry guard existed.
func attemptOf[T river.JobArgs](job *river.Job[T]) int {
	if job == nil || job.JobRow == nil {
		return 1
	}
	return job.Attempt
}

// refusesRetry reports whether a job attempt must not re-execute, given the run
// status recorded before this attempt started.
//
// "applying" is the status this worker sets for exactly the mutating operations
// (apply, destroy, import, test), so seeing it on a retry means a previous
// attempt reached execution and may have changed infrastructure. "planning"
// stays retryable because a plan writes nothing, and "pending"/"queued" mean the
// first attempt never got past UpdateRunStarted.
func refusesRetry(attempt int, priorStatus string) bool {
	return attempt > 1 && priorStatus == "applying"
}

func (w *RunJobWorker) Work(ctx context.Context, job *river.Job[RunJobArgs]) error {
	args := job.Args
	logger := slog.With("run_id", args.RunID, "workspace_id", args.WorkspaceID, "operation", args.Operation)
	logger.Info("starting run job")

	// The workspace slot is already claimed for this run at enqueue time
	// (RunService.Create / ClaimAndEnqueueNextRun), so there's no lock to take
	// here — re-setting it unconditionally would let a job that somehow outlived
	// its claim steal the slot from whoever holds it now.

	// A retry must not re-run a mutating operation that already executed.
	//
	// Only two failures inside this job reach River as errors after tofu has run:
	// the UpdateRunFinished write below, and failRun's own write when marking the
	// run errored fails. Both leave the row exactly as the pre-execution failure
	// path does not — at "applying" — because the write that would have moved it
	// off "applying" is the one that failed. A worker killed mid-apply lands in
	// the same state via River's rescue path.
	//
	// So the prior status discriminates precisely: "pending"/"queued" means the
	// first attempt never got past UpdateRunStarted and re-running is safe, while
	// "applying" means a previous attempt reached execution. "applying" is set
	// for exactly the mutating operations (apply, destroy, import, test), so the
	// status alone carries the distinction and no operation list can drift from
	// it. "planning" is left retryable — a plan writes nothing.
	//
	// Refusing here costs a re-run someone triggers deliberately. Not refusing
	// costs a second destroy.
	if attempt := attemptOf(job); attempt > 1 {
		if prior, err := w.queries.GetRun(ctx, repository.GetRunParams{ID: args.RunID, OrgID: args.OrgID}); err == nil && refusesRetry(attempt, prior.Status) {
			return w.failRun(ctx, args, logger, fmt.Errorf(
				"refusing to retry %s: a previous attempt was already executing and may have changed infrastructure. "+
					"Inspect the run log and the workspace state, then start a new run if the operation still needs to happen",
				args.Operation), "")
		}
	}

	// Update run status
	run, err := w.queries.UpdateRunStarted(ctx, repository.UpdateRunStartedParams{ID: args.RunID, Status: startedStatusFor(args.Operation)})
	if err != nil {
		return fmt.Errorf("failed to update run started: %w", err)
	}

	// The configuration to execute comes off the run row, where RunService.Create
	// froze it, not off the workspace. The workspace is still read for the
	// approval-gate decision after a plan, which is a question about the
	// workspace as it stands now, not about the tree this run holds.
	//
	// An unpinned row can only be one written outside the run service (seeded
	// demo history), and there is no safe guess for what it meant to run — the
	// only fallback available is the live workspace, which is exactly what the
	// pin exists to stop the worker reading. So it fails.
	if run.ConfigSource == "" {
		return w.failRun(ctx, args, logger, errors.New("run has no pinned configuration"), "")
	}

	// Get workspace
	workspace, err := w.queries.GetWorkspace(ctx, repository.GetWorkspaceParams{ID: args.WorkspaceID, OrgID: args.OrgID})
	if err != nil {
		return w.failRun(ctx, args, logger, fmt.Errorf("failed to get workspace: %w", err), "")
	}

	// Load and merge variables from all scopes: org < pipeline < workspace
	var orgExecVars []executor.Variable
	orgVars, _ := w.queries.ListOrgVariables(ctx, args.OrgID)
	for _, v := range orgVars {
		value := v.Value
		if v.Sensitive && w.encryptor != nil {
			decrypted, err := w.encryptor.Decrypt(v.Value)
			if err != nil {
				logger.Warn("failed to decrypt org variable, skipping", "key", v.Key, "error", err)
				continue
			}
			value = decrypted
		}
		orgExecVars = append(orgExecVars, executor.Variable{Key: v.Key, Value: value, Category: v.Category})
	}

	var pipelineExecVars []executor.Variable
	if prs, err := w.queries.GetPipelineRunStageByRunID(ctx, args.RunID); err == nil {
		if pr, err := w.queries.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: prs.PipelineRunID, OrgID: args.OrgID}); err == nil {
			pVars, _ := w.queries.ListPipelineVariables(ctx, repository.ListPipelineVariablesParams{
				PipelineID: pr.PipelineID, OrgID: args.OrgID,
			})
			for _, v := range pVars {
				value := v.Value
				if v.Sensitive && w.encryptor != nil {
					decrypted, err := w.encryptor.Decrypt(v.Value)
					if err != nil {
						logger.Warn("failed to decrypt pipeline variable, skipping", "key", v.Key, "error", err)
						continue
					}
					value = decrypted
				}
				pipelineExecVars = append(pipelineExecVars, executor.Variable{Key: v.Key, Value: value, Category: v.Category})
			}
		}
	}

	vars, err := w.queries.ListWorkspaceVariables(ctx, repository.ListWorkspaceVariablesParams{
		WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
	})
	if err != nil {
		return w.failRun(ctx, args, logger, fmt.Errorf("failed to load variables: %w", err), "")
	}
	var wsExecVars []executor.Variable
	for _, v := range vars {
		value := v.Value
		if v.Sensitive && w.encryptor != nil {
			decrypted, err := w.encryptor.Decrypt(v.Value)
			if err != nil {
				return w.failRun(ctx, args, logger, fmt.Errorf("failed to decrypt variable %q: %w", v.Key, err), "")
			}
			value = decrypted
		}
		wsExecVars = append(wsExecVars, executor.Variable{Key: v.Key, Value: value, Category: v.Category})
	}

	execVars := mergeVariables(orgExecVars, pipelineExecVars, wsExecVars)

	// Fetch previous state from S3 for continuity
	var previousState []byte
	if w.storage != nil {
		latestSV, err := w.queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
			WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
		})
		if err == nil && latestSV.Serial > 0 {
			// Try raw state first (preserves encryption), fall back to browse state
			rawKey := fmt.Sprintf("state-raw/%s/%d.tfstate", args.WorkspaceID, latestSV.Serial)
			if stateData, err := w.storage.GetRawState(ctx, rawKey); err == nil {
				previousState = stateData
				logger.Info("fetched previous state (raw)", "serial", latestSV.Serial, "size", len(stateData))
			} else if latestSV.StateURL != "" {
				if stateData, err := w.storage.GetState(ctx, latestSV.StateURL); err == nil {
					previousState = stateData
					logger.Info("fetched previous state (browse)", "serial", latestSV.Serial, "size", len(stateData))
				} else {
					logger.Warn("failed to fetch previous state", "error", err)
				}
			}
		}
	}

	// Download config archive for upload workspaces
	var archiveData []byte
	if run.ConfigSource == "upload" && run.ConfigVersionID != "" && w.storage != nil {
		key := fmt.Sprintf("configs/%s/%s.tar.gz", args.WorkspaceID, run.ConfigVersionID)
		data, err := w.storage.GetConfigArchive(ctx, key)
		if err != nil {
			return w.failRun(ctx, args, logger, fmt.Errorf("failed to download config archive: %w", err), "")
		}
		archiveData = data
		logger.Info("downloaded config archive", "config_version", run.ConfigVersionID, "size", len(data))
	}

	// Derive state encryption passphrase if encryption is configured
	var stateEncPassphrase string
	if w.encryptor != nil {
		stateEncPassphrase = w.encryptor.DerivePassphrase("state:" + args.WorkspaceID)
	}

	// Collect log output for storage
	var logBuf strings.Builder
	logCallback := func(line []byte) {
		logBuf.Write(line)
		w.streamer.Publish(args.RunID, line)
	}

	// The commit to execute. For a VCS run the branch on the run row is only
	// half a pin: a branch moves, and the window between a plan parking at
	// awaiting_approval and the apply that follows the signature is exactly when
	// it moves. run.commit_sha is what closes that — set by the VCS trigger for
	// a webhook run, and otherwise filled in below from the commit the first
	// execution of this run resolved. Either way the apply re-runs the same run
	// row, so it reads the same pin the plan was produced under.
	//
	// A value that is not an object id fails the run. There is no safe reading
	// of it: ignoring it applies branch head, which is the tree nobody planned.
	pinnedCommit := ""
	if run.ConfigSource == "vcs" && run.CommitSHA != "" {
		if !executor.IsCommitSHA(run.CommitSHA) {
			return w.failRun(ctx, args, logger,
				fmt.Errorf("run is pinned to %q, which is not a git commit id", run.CommitSHA), "")
		}
		pinnedCommit = run.CommitSHA
	}

	// Execute
	execStart := time.Now()
	result, err := w.executor.Execute(ctx, executor.ExecuteParams{
		RunID:                     args.RunID,
		WorkspaceID:               args.WorkspaceID,
		Operation:                 args.Operation,
		RepoURL:                   run.ConfigRepoURL,
		RepoBranch:                run.ConfigRepoBranch,
		CommitSHA:                 pinnedCommit,
		WorkingDir:                run.ConfigWorkingDir,
		TofuVersion:               run.ConfigTofuVersion,
		Variables:                 execVars,
		LogCallback:               logCallback,
		PreviousState:             previousState,
		StateEncryptionPassphrase: stateEncPassphrase,
		Source:                    run.ConfigSource,
		ArchiveData:               archiveData,
		ImportResources:           toExecutorImports(args.Imports),
	})

	execStatus := "success"
	if err != nil {
		execStatus = "error"
	}
	metrics.ObserveTofuRun(args.Operation, execStatus, time.Since(execStart))

	if err != nil {
		// Save partial state if the executor captured it (e.g. failed apply with some resources created).
		// Terragrunt mode: no local StateFile, but `state pull` populates StateJSON.
		if result != nil && (result.StateFile != nil || result.StateJSON != nil) && w.storage != nil {
			latestSV, _ := w.queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
				WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
			})
			nextSerial := latestSV.Serial + 1

			if result.StateFile != nil {
				if _, storeErr := w.storage.PutRawState(ctx, args.WorkspaceID, int(nextSerial), result.StateFile); storeErr != nil {
					logger.Error("failed to upload partial raw state", "error", storeErr)
				}
			}

			browseState := selectBrowseState(result.StateFile, result.StateJSON)
			if len(browseState) > 0 {
				if stateURL, storeErr := w.storage.PutState(ctx, args.WorkspaceID, int(nextSerial), browseState); storeErr != nil {
					logger.Error("failed to upload partial state", "error", storeErr)
				} else {
					w.queries.CreateStateVersion(ctx, repository.CreateStateVersionParams{
						ID: ulid.Make().String(), WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
						RunID: args.RunID, Serial: nextSerial, StateURL: stateURL,
						ResourceCount: 0, ResourceSummary: "partial (errored)",
					})
					logger.Info("saved partial state from failed run", "serial", nextSerial)
				}
			}
		}
		return w.failRun(ctx, args, logger, err, logBuf.String())
	}

	// Determine final status
	finalStatus := "planned"
	if args.Operation == "apply" || args.Operation == "destroy" || args.Operation == "import" {
		finalStatus = "applied"
	} else if args.Operation == "test" {
		finalStatus = "applied" // test is terminal — "applied" prevents approval flow
	} else if args.Operation == "plan" {
		autoApply := workspace.AutoApply
		if args.AutoApplyOverride != nil {
			autoApply = *args.AutoApplyOverride
		}
		finalStatus = postPlanAction(autoApply, workspace.RequiresApproval)
	}

	// Pin the run to the commit it just executed, if it wasn't already pinned.
	// This is what makes an approval mean a tree: the admin reads a plan of this
	// commit, and the apply that follows re-runs this same run row, so it reads
	// this same commit back out even if the branch has moved. PinRunCommitSHA
	// only writes an empty column, so a webhook-supplied pin is never rewritten
	// by what the checkout resolved.
	//
	// If the write fails and an apply is going to follow off this row — a plan
	// parked for approval, or one queued for auto-apply — the run fails instead:
	// an unpinned approvable run is the moving target this exists to remove.
	if pinnedCommit == "" && result.CommitSHA != "" {
		if err := w.queries.PinRunCommitSHA(ctx, args.RunID, result.CommitSHA); err != nil {
			if finalStatus == "awaiting_approval" || finalStatus == "queued" {
				return w.failRun(ctx, args, logger,
					fmt.Errorf("failed to pin run to commit %s: %w", result.CommitSHA, err), logBuf.String())
			}
			logger.Error("failed to record executed commit", "commit", result.CommitSHA, "error", err)
		}
	}

	// Upload logs to S3
	if w.storage != nil {
		phase := args.Operation
		logURL, err := w.storage.PutLog(ctx, args.RunID, phase, []byte(logBuf.String()))
		if err != nil {
			logger.Error("failed to upload logs", "error", err)
		} else {
			planLog := &logURL
			var applyLog *string
			if args.Operation != "plan" && args.Operation != "test" {
				applyLog = planLog
				planLog = nil
			}
			if _, err := w.queries.UpdateRunLogURLs(ctx, repository.UpdateRunLogURLsParams{
				ID: args.RunID, PlanLogURL: planLog, ApplyLogURL: applyLog,
			}); err != nil {
				logger.Error("failed to update run log URLs", "error", err)
			}
		}
	}

	// Upload JSON plan to S3 if available
	if len(result.PlanJSON) > 0 && w.storage != nil {
		planJSONURL, err := w.storage.PutPlanJSON(ctx, args.RunID, result.PlanJSON)
		if err != nil {
			logger.Error("failed to upload plan JSON", "error", err)
		} else {
			if err := w.queries.UpdateRunPlanJSONURL(ctx, repository.UpdateRunPlanJSONURLParams{
				ID: args.RunID, PlanJSONURL: planJSONURL,
			}); err != nil {
				logger.Error("failed to update run plan JSON URL", "error", err)
			}
		}
	}

	// Upload state to S3 after apply/destroy. Terragrunt workspaces don't
	// produce a local terraform.tfstate at the leaf (state lives in their
	// remote backend), so StateFile is empty — fall through on StateJSON
	// alone (which the worker captures via `state pull`).
	if (result.StateFile != nil || result.StateJSON != nil) && w.storage != nil {
		latestSV, _ := w.queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
			WorkspaceID: args.WorkspaceID, OrgID: args.OrgID,
		})
		nextSerial := latestSV.Serial + 1

		// Store raw state (may be encrypted) for restoration on next run.
		// Only present in plain-tofu mode; terragrunt-managed state isn't
		// restored from portal (terragrunt owns its backend).
		if result.StateFile != nil {
			if _, err := w.storage.PutRawState(ctx, args.WorkspaceID, int(nextSerial), result.StateFile); err != nil {
				logger.Error("failed to upload raw state", "error", err)
			}
		}

		// Store decrypted JSON for the resource browser + pipeline output
		// import (fall back to raw if no decrypted version).
		browseState := selectBrowseState(result.StateFile, result.StateJSON)

		if len(browseState) > 0 {
			stateURL, err := w.storage.PutState(ctx, args.WorkspaceID, int(nextSerial), browseState)
			if err != nil {
				logger.Error("failed to upload state", "error", err)
			} else {
				if _, err := w.queries.CreateStateVersion(ctx, repository.CreateStateVersionParams{
					ID:              ulid.Make().String(),
					WorkspaceID:     args.WorkspaceID,
					OrgID:           args.OrgID,
					RunID:           args.RunID,
					Serial:          nextSerial,
					StateURL:        stateURL,
					ResourceCount:   result.ResourcesAdded + result.ResourcesChanged,
					ResourceSummary: fmt.Sprintf("+%d ~%d -%d", result.ResourcesAdded, result.ResourcesChanged, result.ResourcesDeleted),
				}); err != nil {
					logger.Error("failed to create state version", "error", err)
				}
			}
		}
	}

	// Check if run was cancelled while we were executing
	if w.isRunCancelled(ctx, args.RunID, args.OrgID) {
		logger.Info("run was cancelled during execution, skipping status update")
		w.streamer.Publish(args.RunID, []byte("\r\n\033[33mRun was cancelled\033[0m\r\n"))
		w.streamer.Close(args.RunID)
		if err := w.queries.ReleaseWorkspaceRun(ctx, args.WorkspaceID, args.OrgID, args.RunID); err != nil {
			logger.Error("failed to release workspace run slot after cancel", "error", err)
		}
		w.enqueueNextPendingRun(ctx, args.WorkspaceID, logger)
		return nil
	}

	// Update run as finished — return the error so River can retry if DB fails
	if _, err := w.queries.UpdateRunFinished(ctx, repository.UpdateRunFinishedParams{
		ID:               args.RunID,
		Status:           finalStatus,
		PlanOutput:       &result.Output,
		ResourcesAdded:   &result.ResourcesAdded,
		ResourcesChanged: &result.ResourcesChanged,
		ResourcesDeleted: &result.ResourcesDeleted,
	}); err != nil {
		return fmt.Errorf("failed to update run finished: %w", err)
	}

	w.streamer.Publish(args.RunID, []byte(fmt.Sprintf("\r\n\033[32mRun completed successfully at %s\033[0m\r\n", time.Now().Format(time.RFC3339))))
	w.streamer.Close(args.RunID)

	// Auto-apply: enqueue apply job immediately instead of unlocking
	if finalStatus == "queued" && w.riverClient != nil && w.db != nil {
		tx, txErr := w.db.Begin(ctx)
		if txErr != nil {
			logger.Error("auto-apply: begin tx", "error", txErr)
		} else {
			_, insErr := w.riverClient.InsertTx(ctx, tx, RunJobArgs{
				RunID:       args.RunID,
				WorkspaceID: args.WorkspaceID,
				OrgID:       args.OrgID,
				Operation:   "apply",
			}, nil)
			if insErr != nil {
				_ = tx.Rollback(ctx)
				logger.Error("auto-apply: insert job", "error", insErr)
			} else if commitErr := tx.Commit(ctx); commitErr != nil {
				logger.Error("auto-apply: commit", "error", commitErr)
			} else {
				logger.Info("auto-apply enqueued", "run_id", args.RunID)
				return nil
			}
		}
	}

	// Unlock workspace and pick up next queued run
	if err := w.queries.ReleaseWorkspaceRun(ctx, args.WorkspaceID, args.OrgID, args.RunID); err != nil {
		logger.Error("failed to release workspace run slot", "error", err)
	}

	w.enqueueNextPendingRun(ctx, args.WorkspaceID, logger)

	// Advance pipeline if this run belongs to one
	w.advancePipelineIfNeeded(ctx, args.RunID, args.OrgID, finalStatus, logger)

	logger.Info("run completed", "status", finalStatus)
	return nil
}

func (w *RunJobWorker) failRun(ctx context.Context, args RunJobArgs, logger *slog.Logger, runErr error, logOutput string) error {
	logger.Error("run failed", "error", runErr)

	// Job ctx may already be cancelled; status + slot release still need to land.
	writeCtx, cancel := durableContext(ctx)
	defer cancel()

	// Don't overwrite cancelled status
	if !w.isRunCancelled(writeCtx, args.RunID, args.OrgID) {
		errMsg := runErr.Error()
		var planOutput *string
		if logOutput != "" {
			planOutput = &logOutput
		}
		if _, dbErr := w.queries.UpdateRunFinished(writeCtx, repository.UpdateRunFinishedParams{
			ID: args.RunID, Status: "errored", ErrorMessage: &errMsg, PlanOutput: planOutput,
		}); dbErr != nil {
			// Return the DB error so River retries — the run would be stuck otherwise
			return fmt.Errorf("failed to mark run as errored (original error: %v): %w", runErr, dbErr)
		}
		w.streamer.Publish(args.RunID, []byte(fmt.Sprintf("\r\n\033[31mRun failed: %s\033[0m\r\n", runErr.Error())))
	} else {
		logger.Info("run was cancelled, not overwriting with errored status")
		w.streamer.Publish(args.RunID, []byte("\r\n\033[33mRun was cancelled\033[0m\r\n"))
	}
	w.streamer.Close(args.RunID)

	// Unlock workspace
	if err := w.queries.ReleaseWorkspaceRun(writeCtx, args.WorkspaceID, args.OrgID, args.RunID); err != nil {
		logger.Error("failed to release workspace run slot after failure", "error", err)
	}

	w.enqueueNextPendingRun(writeCtx, args.WorkspaceID, logger)
	return nil
}

// isRunCancelled checks if the run status was set to cancelled (e.g. via the API)
// while the worker was executing. Returns true if the run should not have its status overwritten.
func (w *RunJobWorker) isRunCancelled(ctx context.Context, runID, orgID string) bool {
	currentRun, err := w.queries.GetRun(ctx, repository.GetRunParams{ID: runID, OrgID: orgID})
	if err != nil {
		return false
	}
	return currentRun.Status == "cancelled"
}

// postPlanAction determines the status after a plan completes.
// requires_approval wins over auto_apply. "queued" triggers auto-apply enqueue.
// selectBrowseState returns the bytes that should be uploaded as the
// resource-browser / pipeline-output state for the run, or nil when there
// is nothing to capture. In plain-tofu mode the executor produces both
// StateFile (raw on-disk state) and StateJSON (decrypted JSON for the
// browser). In terragrunt mode, state lives in the remote backend — there
// is no leaf-side StateFile to capture, but `tofu state pull` populates
// StateJSON. The browse path prefers StateJSON when present; raw
// StateFile is the fallback so plain-tofu runs without StateJSON still
// land a row.
func selectBrowseState(stateFile, stateJSON []byte) []byte {
	if len(stateJSON) > 0 {
		return stateJSON
	}
	return stateFile
}

func postPlanAction(autoApply, requiresApproval bool) string {
	// requires_approval is checked first, and wins. It is the workspace's
	// statement that no apply happens without a human signing off, so it has to
	// outrank auto_apply — otherwise auto_apply is a way to skip the approval
	// rather than a convenience on workspaces that do not need one. A pipeline
	// stage supplies auto_apply as a per-run override (AutoApplyOverride), so
	// with the old ordering a stage could turn the gate off for a workspace
	// whose owner had turned it on.
	if requiresApproval {
		return "awaiting_approval"
	}
	if autoApply {
		return "queued"
	}
	return "planned"
}

func toExecutorImports(imports []ImportResource) []executor.ImportResource {
	if len(imports) == 0 {
		return nil
	}
	result := make([]executor.ImportResource, len(imports))
	for i, imp := range imports {
		result[i] = executor.ImportResource{Address: imp.Address, ID: imp.ID}
	}
	return result
}

// advancePipelineIfNeeded checks if the completed run belongs to a pipeline and advances it.
func (w *RunJobWorker) advancePipelineIfNeeded(ctx context.Context, runID, orgID, finalStatus string, logger *slog.Logger) {
	stage, err := w.pipelines.GetPipelineRunStageByRunID(ctx, runID)
	if err != nil {
		return // not a pipeline run — fast no-op
	}

	pr, err := w.pipelines.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: stage.PipelineRunID, OrgID: orgID})
	if err != nil {
		logger.Error("failed to get pipeline run for callback", "error", err)
		return
	}

	if pr.Status != "running" {
		return
	}

	logger = logger.With("pipeline_run_id", pr.ID, "stage_order", stage.StageOrder)

	switch finalStatus {
	case "applied":
		w.finishStage(ctx, stage.ID, "completed", logger)

		nextOrder := stage.StageOrder + 1
		if nextOrder >= pr.TotalStages {
			if w.finishRun(ctx, pr.ID, "completed", logger) {
				logger.Info("pipeline completed")
			}
			return
		}
		w.enqueueStage(ctx, pr, nextOrder, orgID, logger)

	case "errored":
		w.finishStage(ctx, stage.ID, "errored", logger)
		if stage.OnFailure == "continue" {
			nextOrder := stage.StageOrder + 1
			if nextOrder >= pr.TotalStages {
				w.finishRun(ctx, pr.ID, "errored", logger)
				return
			}
			w.enqueueStage(ctx, pr, nextOrder, orgID, logger)
		} else {
			// Cancel first: a pipeline marked errored while its later stages still
			// read "pending" is a pipeline the UI shows as having work outstanding
			// that nothing will ever pick up.
			if err := w.pipelines.CancelPendingPipelineRunStages(ctx, pr.ID); err != nil {
				logger.Error("failed to cancel pending pipeline stages", "error", err)
			}
			if w.finishRun(ctx, pr.ID, "errored", logger) {
				logger.Info("pipeline errored due to stage failure")
			}
		}

	case "planned", "awaiting_approval":
		// Pipeline pauses — update stage status. A failure leaves the stage
		// reading "running" while the run waits for an approval, so the UI shows
		// work in progress and the approval it needs is attached to a stage
		// nobody is looking at.
		if _, err := w.pipelines.UpdatePipelineRunStageStatus(ctx, repository.UpdatePipelineRunStageStatusParams{
			ID: stage.ID, Status: "awaiting_approval",
		}); err != nil {
			logger.Error("failed to mark pipeline stage awaiting approval", "stage_id", stage.ID, "error", err)
			return
		}
		logger.Info("pipeline paused at stage awaiting approval")

	case "queued":
		// Auto-apply was triggered, stage still running — no action needed

	default:
		logger.Info("unhandled pipeline run status", "final_status", finalStatus)
	}
}

// mergeVariables combines variables from three scopes. Later scopes override earlier.
// Precedence: org < pipeline < workspace (workspace always wins).
// Special case: "tags" (category terraform) is deep-merged as a JSON map across scopes.
func mergeVariables(orgVars, pipelineVars, workspaceVars []executor.Variable) []executor.Variable {
	merged := make(map[string]executor.Variable)
	for _, v := range orgVars {
		merged[v.Key+"|"+v.Category] = v
	}
	for _, v := range pipelineVars {
		mergeVar(merged, v)
	}
	for _, v := range workspaceVars {
		mergeVar(merged, v)
	}
	result := make([]executor.Variable, 0, len(merged))
	for _, v := range merged {
		result = append(result, v)
	}
	return result
}

func mergeVar(merged map[string]executor.Variable, v executor.Variable) {
	key := v.Key + "|" + v.Category
	existing, exists := merged[key]
	v.Value = varmerge.Layer(v.Key, v.Category, existing.Value, v.Value, exists)
	merged[key] = v
}

// finishStage records a stage's terminal status. A failure cannot be retried —
// the run row is already final and the job is not re-run — so the stage is left
// reading "running" under a pipeline that has moved on, and only this line says
// so.
func (w *RunJobWorker) finishStage(ctx context.Context, stageID, status string, logger *slog.Logger) {
	if _, err := w.pipelines.FinishPipelineRunStage(ctx, stageID, status); err != nil {
		logger.Error("failed to finish pipeline stage", "stage_id", stageID, "status", status, "error", err)
	}
}

// finishRun records the pipeline's terminal status and reports whether it landed.
//
// The caller uses the return value to decide whether to announce completion: a
// run that failed to finish has not completed, and saying so in the log while
// the row still reads "running" is how an operator ends up trusting the log over
// the database. The row staying "running" also blocks every later run of that
// pipeline, and nothing sweeps it.
func (w *RunJobWorker) finishRun(ctx context.Context, pipelineRunID, status string, logger *slog.Logger) bool {
	if _, err := w.pipelines.FinishPipelineRun(ctx, pipelineRunID, status); err != nil {
		logger.Error("failed to finish pipeline run", "status", status, "error", err,
			"consequence", "the run stays 'running' and blocks later runs of this pipeline until it is cleared by hand")
		return false
	}
	return true
}

// enqueueStage inserts the next stage's job in a transaction.
//
// One implementation for both the success path and the continue-on-failure path.
// They used to be separate copies of the same three calls, and only one of the
// copies checked them: a failed insert on the continue path left the pipeline
// with no next stage and no record of why, which is indistinguishable from a
// pipeline still working.
func (w *RunJobWorker) enqueueStage(ctx context.Context, pr repository.PipelineRun, nextOrder int32, orgID string, logger *slog.Logger) {
	if w.riverClient == nil || w.db == nil {
		return
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		logger.Error("failed to begin tx for next pipeline stage", "next_order", nextOrder, "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := w.riverClient.InsertTx(ctx, tx, PipelineStageJobArgs{
		PipelineRunID: pr.ID,
		StageOrder:    nextOrder,
		OrgID:         orgID,
		CreatedBy:     pr.CreatedBy,
	}, nil); err != nil {
		logger.Error("failed to enqueue next pipeline stage", "next_order", nextOrder, "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		logger.Error("failed to commit next pipeline stage", "next_order", nextOrder, "error", err)
		return
	}
	logger.Info("enqueued next pipeline stage", "next_order", nextOrder)
}

func (w *RunJobWorker) enqueueNextPendingRun(ctx context.Context, workspaceID string, logger *slog.Logger) {
	ClaimAndEnqueueNextRun(ctx, w.queries, w.db, w.riverClient, workspaceID, logger)
}

// ClaimAndEnqueueNextRun atomically claims the workspace's run slot for the
// oldest pending run and enqueues it, in one transaction. It's a no-op when the
// slot is already held or nothing is pending. Safe to call from multiple paths
// at once (a worker finishing, an API cancel): the conditional claim guarantees
// only one caller can take the slot, so the same run can't be enqueued twice.
func ClaimAndEnqueueNextRun(ctx context.Context, q *repository.Queries, db *pgxpool.Pool, riverClient *river.Client[pgx.Tx], workspaceID string, logger *slog.Logger) {
	if riverClient == nil || db == nil {
		return
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		logger.Error("failed to begin tx for next pending run", "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := q.WithTx(tx)

	nextRun, err := qtx.GetNextPendingRun(ctx, workspaceID)
	if err != nil {
		return // no pending runs (pgx.ErrNoRows)
	}

	// Take the slot atomically. If another path already claimed it, back off and
	// let that run proceed — this pending run is picked up on the next release.
	if _, err := qtx.ClaimWorkspaceForRun(ctx, workspaceID, nextRun.OrgID, nextRun.ID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Error("failed to claim workspace for next run", "error", err, "run_id", nextRun.ID)
		}
		return
	}

	if _, err := riverClient.InsertTx(ctx, tx, RunJobArgs{
		RunID:       nextRun.ID,
		WorkspaceID: nextRun.WorkspaceID,
		OrgID:       nextRun.OrgID,
		Operation:   nextRun.Operation,
	}, nil); err != nil {
		logger.Error("failed to enqueue next pending run", "error", err, "run_id", nextRun.ID)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("failed to commit next pending run", "error", err, "run_id", nextRun.ID)
		return
	}

	logger.Info("enqueued next pending run", "run_id", nextRun.ID)
}
