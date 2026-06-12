import { useState } from "react";
import { useForm, useWatch, type Resolver } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import type { CreateWorkspaceRequest } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { GitBranch, Upload } from "lucide-react";

const vcsSchema = z.object({
  source: z.literal("vcs"),
  name: z.string().min(1, "Name is required").max(64),
  description: z.string().max(256).optional(),
  repo_url: z.string().url("Must be a valid URL"),
  repo_branch: z.string().default("main"),
  working_dir: z.string().default("."),
  tofu_version: z.string().default("1.11.0"),
  environment: z.enum(["development", "staging", "production"]).default("development"),
  auto_apply: z.boolean().default(false),
  requires_approval: z.boolean().default(false),
});

const uploadSchema = z.object({
  source: z.literal("upload"),
  name: z.string().min(1, "Name is required").max(64),
  description: z.string().max(256).optional(),
  working_dir: z.string().default("."),
  tofu_version: z.string().default("1.11.0"),
  environment: z.enum(["development", "staging", "production"]).default("development"),
  auto_apply: z.boolean().default(false),
  requires_approval: z.boolean().default(false),
});

interface Props {
  open: boolean;
  onClose: () => void;
  onSubmit: (data: CreateWorkspaceRequest) => void;
  isLoading: boolean;
}

export function CreateWorkspaceDialog({
  open,
  onClose,
  onSubmit,
  isLoading,
}: Props) {
  const [source, setSource] = useState<"vcs" | "upload">("vcs");

  const schema = source === "vcs" ? vcsSchema : uploadSchema;

  const {
    register,
    handleSubmit,
    reset,
    control,
    setValue,
    formState: { errors },
  } = useForm<CreateWorkspaceRequest>({
    // schema is one of two concrete object schemas at runtime; the conditional
    // union doesn't unify through useForm's generic, so type the resolver.
    resolver: zodResolver(schema) as Resolver<CreateWorkspaceRequest>,
    defaultValues: {
      source,
      repo_branch: "main",
      working_dir: ".",
      tofu_version: "1.11.0",
      environment: "development",
      auto_apply: false,
      requires_approval: false,
    },
  });

  const environment = useWatch({ control, name: "environment" });

  const handleClose = () => {
    reset();
    setSource("vcs");
    onClose();
  };

  const handleSourceChange = (s: "vcs" | "upload") => {
    setSource(s);
    reset({
      source: s,
      repo_branch: "main",
      working_dir: ".",
      tofu_version: "1.11.0",
      environment: "development",
      auto_apply: false,
      requires_approval: false,
    });
  };

  return (
    <Dialog open={open} onClose={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create workspace</DialogTitle>
          <DialogDescription>
            {source === "vcs"
              ? "Connect a Git repository to manage OpenTofu infrastructure."
              : "Upload .tf files to manage OpenTofu infrastructure."}
          </DialogDescription>
        </DialogHeader>

        <form
          onSubmit={handleSubmit((data) => onSubmit({ ...data, source }))}
          className="space-y-4"
        >
          {/* Source toggle */}
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => handleSourceChange("vcs")}
              className={`flex-1 flex items-center justify-center gap-2 px-3 py-2.5 rounded-lg border text-sm font-medium transition-all cursor-pointer ${
                source === "vcs"
                  ? "border-primary bg-primary/10 text-primary"
                  : "border-border text-muted-foreground hover:border-primary/30"
              }`}
            >
              <GitBranch className="w-4 h-4" />
              VCS (Git)
            </button>
            <button
              type="button"
              onClick={() => handleSourceChange("upload")}
              className={`flex-1 flex items-center justify-center gap-2 px-3 py-2.5 rounded-lg border text-sm font-medium transition-all cursor-pointer ${
                source === "upload"
                  ? "border-primary bg-primary/10 text-primary"
                  : "border-border text-muted-foreground hover:border-primary/30"
              }`}
            >
              <Upload className="w-4 h-4" />
              Upload
            </button>
          </div>

          <div>
            <label className="block text-sm font-medium mb-1.5">Name</label>
            <Input
              {...register("name")}
              placeholder="my-infrastructure"
              autoFocus
            />
            {errors.name && (
              <p className="text-xs text-destructive mt-1">
                {errors.name.message}
              </p>
            )}
          </div>

          <div>
            <label className="block text-sm font-medium mb-1.5">
              Description
            </label>
            <Input
              {...register("description")}
              placeholder="Production AWS infrastructure"
            />
          </div>

          {source === "vcs" && (
            <>
              <div>
                <label className="block text-sm font-medium mb-1.5">
                  Repository URL
                </label>
                <Input
                  {...register("repo_url")}
                  placeholder="https://github.com/org/repo"
                />
                {errors.repo_url && (
                  <p className="text-xs text-destructive mt-1">
                    {errors.repo_url.message}
                  </p>
                )}
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-sm font-medium mb-1.5">
                    Branch
                  </label>
                  <Input {...register("repo_branch")} placeholder="main" />
                </div>
                <div>
                  <label className="block text-sm font-medium mb-1.5">
                    Working directory
                  </label>
                  <Input {...register("working_dir")} placeholder="." />
                </div>
              </div>
            </>
          )}

          {source === "upload" && (
            <div>
              <label className="block text-sm font-medium mb-1.5">
                Working directory
              </label>
              <Input {...register("working_dir")} placeholder="." />
              <p className="text-xs text-muted-foreground mt-1">
                Subdirectory within the uploaded archive to run tofu in.
              </p>
            </div>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-sm font-medium mb-1.5">
                OpenTofu version
              </label>
              <Input
                {...register("tofu_version")}
                placeholder="1.11.0"
              />
            </div>
            <div>
              <label className="block text-sm font-medium mb-1.5">
                Environment
              </label>
              <Select
                value={environment}
                onChange={(e) => setValue("environment", e.target.value as CreateWorkspaceRequest["environment"])}
              >
                <option value="development">Development</option>
                <option value="staging">Staging</option>
                <option value="production">Production</option>
              </Select>
            </div>
          </div>

          <div className="space-y-3 pt-2 border-t border-border">
            <p className="text-sm font-medium">Workflow</p>
            <label className="flex items-center gap-3 cursor-pointer">
              <input
                type="checkbox"
                {...register("auto_apply")}
                className="w-4 h-4 rounded border-border"
              />
              <div>
                <div className="text-sm">Auto-apply</div>
                <div className="text-xs text-muted-foreground">
                  Automatically apply after a successful plan
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
                <div className="text-sm">Require approval</div>
                <div className="text-xs text-muted-foreground">
                  Require manual approval before applying
                </div>
              </div>
            </label>
          </div>

          {source === "upload" && (
            <div className="rounded-lg border border-border bg-accent/30 p-3">
              <p className="text-xs text-muted-foreground">
                After creating the workspace, upload a <code className="text-xs">.tar.gz</code> archive
                of your .tf files from the workspace detail page.
              </p>
            </div>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <Button
              type="button"
              variant="ghost"
              onClick={handleClose}
              disabled={isLoading}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={isLoading}>
              {isLoading && <Spinner />}
              Create workspace
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
