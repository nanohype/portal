# Pipelines

Pipelines orchestrate sequential workspace deployments with automatic output passing between stages.

## When to Use

Use a pipeline when multiple workspaces must deploy in order, and later stages depend on outputs from earlier ones. Common examples:

- **Landing zone**: network → cluster → cluster-bootstrap → cluster-addons
- **Multi-tier app**: database → backend → frontend
- **Shared infra**: base-network → service-a → service-b (fan-out after shared base)

## Creating a Pipeline

1. Go to **Pipelines** in the sidebar
2. Click **New Pipeline**
3. Name it and add an optional description
4. Add workspace stages in order using the dropdown
5. For each stage, configure:
   - **Auto-apply**: if enabled, the plan automatically applies without waiting for approval. On a workspace that requires approval the stage parks for a signature regardless, and setting the flag there is an admin action; on any other workspace it is the apply an operator may already start by hand, so the toggle is theirs
   - **On failure**: `stop` (default) halts the pipeline, `continue` skips the failed stage and moves on
6. Drag stages to reorder using the grip handle

## Running a Pipeline

1. Open the pipeline detail page
2. Click **Run Pipeline**
3. The pipeline creates a workspace run for each stage in sequence

### Stage Progression

For each stage, the pipeline:

1. **Imports outputs** from the previous stage's workspace state as terraform variables on the current stage's workspace (skipped for the first stage)
2. **Creates a plan run** on the stage's workspace via the normal run system
3. **Waits for the run to complete** — the worker callback advances the pipeline

### What Happens on Completion

| Run result | Pipeline behavior |
|------------|-------------------|
| `applied` | Stage marked completed, next stage enqueued |
| `errored` | Check `on_failure`: `stop` fails the pipeline, `continue` skips to next stage |
| `planned` / `awaiting_approval` | Pipeline pauses until the user approves via the normal approval flow |
| `cancelled` | Stage and pipeline marked cancelled |

### Auto-Apply Override

Each pipeline stage has its own `auto_apply` setting that overrides the workspace's setting for that run. This means a workspace that normally requires manual apply can auto-apply when run as part of a pipeline.

The override does not reach `requires_approval`. A workspace whose owner asked for a human parks the stage at `awaiting_approval` no matter what the stage says — the gate outranks the convenience flag, or auto-apply would be a way around it.

## Pipeline Variables

Set variables on the pipeline (Variables tab on the pipeline detail page) that apply to all stages. These sit between org and workspace in the precedence chain:

```
org variables  <  pipeline variables  <  workspace variables
```

Use pipeline variables for values shared across all stages that aren't org-wide — e.g., `cluster_name`, `vpc_id` that are specific to this pipeline's deployment.

## Cancelling a Pipeline Run

Click **Cancel** on the pipeline run view. This:

1. Cancels the currently running workspace run (if any)
2. Marks all remaining pending stages as cancelled
3. Marks the pipeline run as cancelled

## Output Passing

When stage N+1 starts, the pipeline worker:

1. Fetches the latest state from stage N's workspace
2. Parses the state's outputs
3. Creates or updates terraform variables on stage N+1's workspace

For example, if the `network` workspace outputs `vpc_id` and `subnet_ids`, the `cluster` workspace automatically gets those as terraform variables before its plan runs.

Sensitive outputs are skipped. Non-sensitive outputs are stored as non-sensitive terraform variables with a description noting their source.

## Example: Landing Zone

```
Pipeline: landing-zone
├── Stage 0: network         (auto_apply=true,  on_failure=stop)
├── Stage 1: cluster         (auto_apply=true,  on_failure=stop)
├── Stage 2: cluster-bootstrap (auto_apply=true, on_failure=stop)
└── Stage 3: cluster-addons  (auto_apply=true,  on_failure=continue)
```

1. `network` plans and applies — outputs `vpc_id`, `subnet_ids`
2. `cluster` receives those outputs as variables, plans and applies — outputs `cluster_endpoint`, `cluster_ca`
3. `cluster-bootstrap` receives cluster outputs, installs core components
4. `cluster-addons` receives outputs, installs optional addons — if it fails, the pipeline still completes (on_failure=continue)

## Concurrent Run Protection

Only one pipeline run can be active per pipeline. Starting a new run while one is running returns a 409 Conflict. Deleting a pipeline with active runs is also blocked.
