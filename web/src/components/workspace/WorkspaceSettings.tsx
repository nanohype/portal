import { useState } from "react";
import { useForm, useWatch, type Resolver } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Workspace } from "@/api/models";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { navigate } from "@/hooks/useNavigate";
import { Copy, Check } from "lucide-react";

const vcsSchema = z.object({
  name: z.string().min(1, "Name is required").max(64),
  description: z.string().max(256).optional(),
  repo_url: z.string().url("Must be a valid URL"),
  repo_branch: z.string().min(1),
  working_dir: z.string().min(1),
  tofu_version: z.string().min(1),
  environment: z.enum(["development", "staging", "production"]),
  auto_apply: z.boolean(),
  requires_approval: z.boolean(),
  vcs_trigger_enabled: z.boolean(),
});

const uploadSchema = z.object({
  name: z.string().min(1, "Name is required").max(64),
  description: z.string().max(256).optional(),
  working_dir: z.string().min(1),
  tofu_version: z.string().min(1),
  environment: z.enum(["development", "staging", "production"]),
  auto_apply: z.boolean(),
  requires_approval: z.boolean(),
});

type FormValues = z.infer<typeof vcsSchema>;

interface Props {
  workspace: Workspace;
}

function WebhookURLField() {
  const [copied, setCopied] = useState(false);
  const webhookURL = `${window.location.origin}/api/v1/webhooks/github`;

  const handleCopy = () => {
    navigator.clipboard.writeText(webhookURL);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="rounded-lg border border-border bg-accent/30 p-4 space-y-2">
      <label className="block text-sm font-medium">Webhook URL</label>
      <p className="text-xs text-muted-foreground">
        Add this URL as a webhook in your GitHub repository settings.
        Set the content type to <code className="text-xs">application/json</code> and
        select <code className="text-xs">push</code> events only.
      </p>
      <div className="flex items-center gap-2">
        <Input
          readOnly
          value={webhookURL}
          className="font-mono text-xs flex-1"
          onClick={(e) => (e.target as HTMLInputElement).select()}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleCopy}
          className="shrink-0"
        >
          {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
        </Button>
      </div>
    </div>
  );
}

export function WorkspaceSettings({ workspace }: Props) {
  const queryClient = useQueryClient();
  const [deleteConfirm, setDeleteConfirm] = useState("");
  const isUpload = workspace.source === "upload";
  const schema = isUpload ? uploadSchema : vcsSchema;

  const {
    register,
    handleSubmit,
    control,
    setValue,
    formState: { errors, isDirty },
  } = useForm<FormValues>({
    // schema is one of two concrete object schemas at runtime; the conditional
    // union doesn't unify through useForm's generic, so type the resolver.
    resolver: zodResolver(schema) as unknown as Resolver<FormValues>,
    defaultValues: {
      name: workspace.name,
      description: workspace.description || "",
      repo_url: workspace.repo_url,
      repo_branch: workspace.repo_branch,
      working_dir: workspace.working_dir,
      tofu_version: workspace.tofu_version,
      environment: workspace.environment,
      auto_apply: workspace.auto_apply,
      requires_approval: workspace.requires_approval,
      vcs_trigger_enabled: workspace.vcs_trigger_enabled,
    },
  });

  const environment = useWatch({ control, name: "environment" });

  const updateMutation = useMutation({
    mutationFn: async (data: FormValues) => {
      const { data: result, error } = await api.PUT(
        "/workspaces/{workspaceId}",
        {
          params: { path: { workspaceId: workspace.id } },
          body: data,
        }
      );
      if (error) throw error;
      return result;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace", workspace.id] });
      toast.success("Settings saved");
    },
    onError: () => toast.error("Failed to save settings"),
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/workspaces/{workspaceId}", {
        params: { path: { workspaceId: workspace.id } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      toast.success("Workspace deleted");
      navigate("/");
    },
    onError: () => toast.error("Failed to delete workspace"),
  });

  return (
    <div className="max-w-2xl space-y-8">
      {/* General settings */}
      <form onSubmit={handleSubmit((data) => updateMutation.mutate(data))} className="space-y-4">
        <h3 className="text-lg font-semibold">General</h3>

        <div>
          <label className="block text-sm font-medium mb-1.5">Name</label>
          <Input {...register("name")} />
          {errors.name && (
            <p className="text-xs text-destructive mt-1">{errors.name.message}</p>
          )}
        </div>

        <div>
          <label className="block text-sm font-medium mb-1.5">Description</label>
          <Input {...register("description")} />
        </div>

        {!isUpload && (
          <div>
            <label className="block text-sm font-medium mb-1.5">Repository URL</label>
            <Input {...register("repo_url")} />
            {errors.repo_url && (
              <p className="text-xs text-destructive mt-1">{errors.repo_url.message}</p>
            )}
          </div>
        )}

        <div className={isUpload ? "" : "grid grid-cols-2 gap-3"}>
          {!isUpload && (
            <div>
              <label className="block text-sm font-medium mb-1.5">Branch</label>
              <Input {...register("repo_branch")} />
            </div>
          )}
          <div>
            <label className="block text-sm font-medium mb-1.5">Working directory</label>
            <Input {...register("working_dir")} />
          </div>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-sm font-medium mb-1.5">OpenTofu version</label>
            <Input {...register("tofu_version")} />
          </div>
          <div>
            <label className="block text-sm font-medium mb-1.5">Environment</label>
            <Select
              value={environment}
              onChange={(e) => setValue("environment", e.target.value as FormValues["environment"], { shouldDirty: true })}
            >
              <option value="development">Development</option>
              <option value="staging">Staging</option>
              <option value="production">Production</option>
            </Select>
          </div>
        </div>

        <h3 className="text-lg font-semibold pt-4">Workflow</h3>

        <label className="flex items-center gap-3 cursor-pointer">
          <input
            type="checkbox"
            {...register("auto_apply")}
            className="w-4 h-4 rounded border-border"
          />
          <div>
            <div className="text-sm font-medium">Auto-apply</div>
            <div className="text-xs text-muted-foreground">
              Automatically apply changes after a successful plan
            </div>
          </div>
        </label>

        <label className="flex items-center gap-3 cursor-pointer">
          <input
            type="checkbox"
            {...register("requires_approval")}
            className="w-4 h-4 rounded border-border"
          />
          <div>
            <div className="text-sm font-medium">Require approval</div>
            <div className="text-xs text-muted-foreground">
              Require manual approval before applying changes
            </div>
          </div>
        </label>

        {!isUpload && (
          <>
            <h3 className="text-lg font-semibold pt-4">VCS Integration</h3>

            <label className="flex items-center gap-3 cursor-pointer">
              <input
                type="checkbox"
                {...register("vcs_trigger_enabled")}
                className="w-4 h-4 rounded border-border"
              />
              <div>
                <div className="text-sm font-medium">VCS-driven runs</div>
                <div className="text-xs text-muted-foreground">
                  Automatically trigger a plan when code is pushed to the configured branch
                </div>
              </div>
            </label>

            {workspace.vcs_trigger_enabled && (
              <WebhookURLField />
            )}
          </>
        )}

        <div className="flex justify-end pt-2">
          <Button type="submit" disabled={!isDirty || updateMutation.isPending}>
            {updateMutation.isPending && <Spinner />}
            Save changes
          </Button>
        </div>

      </form>

      {/* Danger zone */}
      <div className="border border-destructive/30 rounded-lg p-6 space-y-4">
        <h3 className="text-lg font-semibold text-destructive">Danger zone</h3>
        <p className="text-sm text-muted-foreground">
          Deleting a workspace is permanent and cannot be undone. All runs,
          state versions, and variables will be lost.
        </p>
        <div>
          <label className="block text-sm font-medium mb-1.5">
            Type <span className="font-mono text-destructive">{workspace.name}</span> to confirm
          </label>
          <Input
            value={deleteConfirm}
            onChange={(e) => setDeleteConfirm(e.target.value)}
            placeholder={workspace.name}
          />
        </div>
        <Button
          variant="outline"
          className="text-destructive hover:bg-destructive/10"
          disabled={deleteConfirm !== workspace.name || deleteMutation.isPending}
          onClick={() => deleteMutation.mutate()}
        >
          {deleteMutation.isPending && <Spinner />}
          Delete workspace
        </Button>
      </div>
    </div>
  );
}
