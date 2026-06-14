-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1 AND org_id = $2;

-- name: ListWorkspaces :many
SELECT * FROM workspaces
WHERE org_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountWorkspaces :one
SELECT COUNT(*) FROM workspaces WHERE org_id = $1;

-- name: CreateWorkspace :one
INSERT INTO workspaces (id, org_id, name, description, repo_url, repo_branch, working_dir, tofu_version, environment, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateWorkspace :one
UPDATE workspaces
SET name = $3, description = $4, repo_url = $5, repo_branch = $6,
    working_dir = $7, tofu_version = $8, environment = $9, updated_at = NOW()
WHERE id = $1 AND org_id = $2
RETURNING *;

-- name: DeleteWorkspace :exec
DELETE FROM workspaces WHERE id = $1 AND org_id = $2;

-- name: LockWorkspace :one
UPDATE workspaces
SET locked = TRUE, locked_by = $3, updated_at = NOW()
WHERE id = $1 AND org_id = $2 AND locked = FALSE
RETURNING *;

-- name: UnlockWorkspace :one
UPDATE workspaces
SET locked = FALSE, locked_by = NULL, updated_at = NOW()
WHERE id = $1 AND org_id = $2
RETURNING *;

-- name: SetWorkspaceCurrentRun :exec
UPDATE workspaces
SET current_run_id = $3, updated_at = NOW()
WHERE id = $1 AND org_id = $2;

-- ClaimWorkspaceForRun atomically takes the single run slot iff it's free —
-- the basis for run serialization (one executing run per workspace).
-- name: ClaimWorkspaceForRun :one
UPDATE workspaces
SET current_run_id = $3, updated_at = NOW()
WHERE id = $1 AND org_id = $2 AND current_run_id IS NULL
RETURNING id;

-- ReleaseWorkspaceRun frees the slot only if runID still holds it, so releasing
-- a non-holder (e.g. a cancelled queued run) can't free the active run's slot.
-- name: ReleaseWorkspaceRun :exec
UPDATE workspaces
SET current_run_id = NULL, updated_at = NOW()
WHERE id = $1 AND org_id = $2 AND current_run_id = $3;

-- name: ListWorkspacesWithSummary :many
SELECT w.*,
       lr.status AS last_run_status,
       lr.created_at AS last_run_at,
       COALESCE(sv.resource_count, 0) AS resource_count
FROM workspaces w
LEFT JOIN LATERAL (
    SELECT status, created_at FROM runs WHERE workspace_id = w.id ORDER BY created_at DESC LIMIT 1
) lr ON true
LEFT JOIN LATERAL (
    SELECT resource_count FROM state_versions WHERE workspace_id = w.id ORDER BY serial DESC LIMIT 1
) sv ON true
WHERE w.org_id = $1
  AND ($4::TEXT = '' OR w.name ILIKE '%' || $4 || '%')
  AND ($5::TEXT = '' OR w.environment = $5)
ORDER BY w.created_at DESC LIMIT $2 OFFSET $3;

-- name: CountWorkspacesFiltered :one
SELECT COUNT(*) FROM workspaces
WHERE org_id = $1
  AND ($2::TEXT = '' OR name ILIKE '%' || $2 || '%')
  AND ($3::TEXT = '' OR environment = $3);
