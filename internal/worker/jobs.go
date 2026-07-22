package worker

import (
	"context"
	"encoding/json"
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

type RunJobWorker struct {
	river.WorkerDefaults[RunJobArgs]
	queries     *repository.Queries
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

func (w *RunJobWorker) Work(ctx context.Context, job *river.Job[RunJobArgs]) error {
	args := job.Args
	logger := slog.With("run_id", args.RunID, "workspace_id", args.WorkspaceID, "operation", args.Operation)
	logger.Info("starting run job")

	// The workspace slot is already claimed for this run at enqueue time
	// (RunService.Create / ClaimAndEnqueueNextRun), so there's no lock to take
	// here — re-setting it unconditionally would let a job that somehow outlived
	// its claim steal the slot from whoever holds it now.

	// Update run status
	status := "planning"
	if args.Operation == "apply" || args.Operation == "destroy" || args.Operation == "import" || args.Operation == "test" {
		status = "applying"
	}
	run, err := w.queries.UpdateRunStarted(ctx, repository.UpdateRunStartedParams{ID: args.RunID, Status: status})
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
		if txErr == nil {
			_, insErr := w.riverClient.InsertTx(ctx, tx, RunJobArgs{
				RunID:       args.RunID,
				WorkspaceID: args.WorkspaceID,
				OrgID:       args.OrgID,
				Operation:   "apply",
			}, nil)
			if insErr == nil {
				tx.Commit(ctx)
				logger.Info("auto-apply enqueued", "run_id", args.RunID)
				return nil
			}
			tx.Rollback(ctx)
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

	// Don't overwrite cancelled status
	if !w.isRunCancelled(ctx, args.RunID, args.OrgID) {
		errMsg := runErr.Error()
		var planOutput *string
		if logOutput != "" {
			planOutput = &logOutput
		}
		if _, dbErr := w.queries.UpdateRunFinished(ctx, repository.UpdateRunFinishedParams{
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
	if err := w.queries.ReleaseWorkspaceRun(ctx, args.WorkspaceID, args.OrgID, args.RunID); err != nil {
		logger.Error("failed to release workspace run slot after failure", "error", err)
	}

	w.enqueueNextPendingRun(ctx, args.WorkspaceID, logger)
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
	stage, err := w.queries.GetPipelineRunStageByRunID(ctx, runID)
	if err != nil {
		return // not a pipeline run — fast no-op
	}

	pr, err := w.queries.GetPipelineRun(ctx, repository.GetPipelineRunParams{ID: stage.PipelineRunID, OrgID: orgID})
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
		// Stage completed successfully
		w.queries.FinishPipelineRunStage(ctx, stage.ID, "completed")

		nextOrder := stage.StageOrder + 1
		if nextOrder >= pr.TotalStages {
			w.queries.FinishPipelineRun(ctx, pr.ID, "completed")
			logger.Info("pipeline completed")
			return
		}

		// Enqueue next stage
		if w.riverClient != nil && w.db != nil {
			tx, err := w.db.Begin(ctx)
			if err != nil {
				logger.Error("failed to begin tx for next pipeline stage", "error", err)
				return
			}
			defer tx.Rollback(ctx)

			_, err = w.riverClient.InsertTx(ctx, tx, PipelineStageJobArgs{
				PipelineRunID: pr.ID,
				StageOrder:    nextOrder,
				OrgID:         orgID,
				CreatedBy:     pr.CreatedBy,
			}, nil)
			if err != nil {
				logger.Error("failed to enqueue next pipeline stage", "error", err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				logger.Error("failed to commit next pipeline stage", "error", err)
				return
			}
			logger.Info("enqueued next pipeline stage", "next_order", nextOrder)
		}

	case "errored":
		w.queries.FinishPipelineRunStage(ctx, stage.ID, "errored")
		if stage.OnFailure == "continue" {
			nextOrder := stage.StageOrder + 1
			if nextOrder >= pr.TotalStages {
				w.queries.FinishPipelineRun(ctx, pr.ID, "errored")
				return
			}
			if w.riverClient != nil && w.db != nil {
				tx, err := w.db.Begin(ctx)
				if err != nil {
					return
				}
				defer tx.Rollback(ctx)
				w.riverClient.InsertTx(ctx, tx, PipelineStageJobArgs{
					PipelineRunID: pr.ID,
					StageOrder:    nextOrder,
					OrgID:         orgID,
					CreatedBy:     pr.CreatedBy,
				}, nil)
				tx.Commit(ctx)
			}
		} else {
			w.queries.CancelPendingPipelineRunStages(ctx, pr.ID)
			w.queries.FinishPipelineRun(ctx, pr.ID, "errored")
			logger.Info("pipeline errored due to stage failure")
		}

	case "planned", "awaiting_approval":
		// Pipeline pauses — update stage status
		w.queries.UpdatePipelineRunStageStatus(ctx, repository.UpdatePipelineRunStageStatusParams{
			ID: stage.ID, Status: "awaiting_approval",
		})
		logger.Info("pipeline paused at stage awaiting approval")

	case "queued":
		// Auto-apply was triggered, stage still running — no action needed

	default:
		logger.Info("unhandled pipeline run status", "final_status", finalStatus)
	}
}

// mergeVariables combines variables from three scopes. Later scopes override earlier.
// Precedence: org < pipeline < workspace (workspace always wins).
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
	if exists && v.Category == "terraform" && isTagsKey(v.Key) {
		// Deep merge JSON maps for tag variables
		if m := deepMergeJSON(existing.Value, v.Value); m != "" {
			v.Value = m
		}
	}
	merged[key] = v
}

// isTagsKey returns true for variables that should be deep-merged as maps.
func isTagsKey(key string) bool {
	return key == "tags" || key == "default_tags" || key == "extra_tags" ||
		strings.HasSuffix(key, "_tags")
}

// deepMergeJSON merges two JSON object strings. Keys in b override keys in a.
func deepMergeJSON(a, b string) string {
	var mapA, mapB map[string]interface{}
	if json.Unmarshal([]byte(a), &mapA) != nil {
		return ""
	}
	if json.Unmarshal([]byte(b), &mapB) != nil {
		return ""
	}
	for k, v := range mapB {
		mapA[k] = v
	}
	out, err := json.Marshal(mapA)
	if err != nil {
		return ""
	}
	return string(out)
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
