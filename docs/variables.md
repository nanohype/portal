# Variables

Variables provide configuration values to OpenTofu runs. Portal supports three scopes with clear inheritance and a special deep-merge behavior for tags.

## Scopes

### Org Variables

Set in **Settings** (sidebar). Apply to every workspace run in the organization.

Use for values shared across all infrastructure: `region`, `account_id`, `team`, `AWS_PROFILE`.

### Pipeline Variables

Set on each pipeline's **Variables** tab. Apply to all stages in that pipeline's runs.

Use for values shared within a pipeline but not org-wide: `cluster_name`, `environment_tier`.

### Workspace Variables

Set on each workspace's **Variables** tab. Most specific scope.

Use for workspace-specific values: `vpc_cidr`, `instance_type`, `enable_monitoring`.

## Precedence

```
org  <  pipeline  <  workspace
```

If the same key + category exists at multiple scopes, the most specific scope wins. A workspace variable `region=eu-west-1` overrides an org variable `region=us-east-1`.

The merge key is `key|category` — a terraform variable `region` and an env variable `region` are independent.

## Effective Variables

On any workspace's Variables tab, click the **Effective** toggle to see the merged result. Each variable shows its source:

- **org** — inherited from organization defaults
- **pipeline** — inherited from a pipeline (pass `pipeline_id` query param)
- **workspace** — set directly on this workspace

This is what the worker will actually pass to the executor at run time.

## Categories

| Category | Label | How it's used |
|----------|-------|---------------|
| `terraform` | Terraform | Written to a `.tfvars` file, passed as tofu input variables |
| `env` | Environment | Set as process environment variables (e.g. `TF_VAR_*`, `AWS_PROFILE`) |

AWS credentials and profiles must use the **Environment** category.

## Tag Deep-Merge

Variables with keys matching `tags`, `default_tags`, `extra_tags`, or any key ending in `_tags` (category: terraform) are deep-merged as JSON maps across scopes instead of replaced.

### Example

| Scope | Value |
|-------|-------|
| org | `{"team": "platform", "managed_by": "portal"}` |
| workspace | `{"app": "network", "team": "networking"}` |
| **merged result** | `{"team": "networking", "managed_by": "portal", "app": "network"}` |

Workspace keys win on conflict (`team` is overridden), but both scopes contribute keys that don't conflict (`managed_by` and `app` are both present).

This follows cloud tagging best practices — org-wide tags (cost center, team, compliance) combine with resource-specific tags without workspaces needing to repeat them.

### Tag Editor

When creating or editing a variable whose key matches the tags pattern, the UI swaps the text input for a key-value pill editor:

- Type a key and value, press Enter or click + to add
- Click X on a pill to remove
- The value is stored as JSON under the hood

## Encryption

Sensitive variables are encrypted with AES-256 before storage. The `ENCRYPTION_KEY` environment variable (exactly 32 bytes) is used to derive the encryption passphrase.

- Values are encrypted in the handler before hitting the database
- Values are decrypted in the worker at run time, right before passing to the executor
- Decryption failure causes the run to fail (no silent fallback)
- Sensitive values are redacted to `***` in API responses and audit logs
- The reveal endpoint (`GET .../variables/{id}/value`) decrypts on demand and logs an audit event

## Variable Discovery

Click **Discover** on the workspace Variables tab to scan the workspace's `.tf` files for `variable` blocks. Shows:

- Variable name, type, description, and default value
- Whether it's already configured
- Whether it's required (no default)

Works with both VCS workspaces (clones the repo) and upload workspaces (extracts the archive from S3).

## Imported Outputs

Pipeline stages automatically import outputs from the previous stage as workspace variables. These are non-sensitive terraform variables with a description noting the source. Since they're workspace-scoped, they override org and pipeline defaults.

The **Import Outputs** button on the Variables tab does the same thing manually — imports outputs from any other workspace's latest state.
