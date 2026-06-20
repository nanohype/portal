import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Run, RunOperation } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { RunStatusBadge } from "@/components/run/RunStatusBadge";
import { isRunInFlight } from "@/lib/status";
import { VariablesPanel } from "@/components/workspace/VariablesPanel";
import { StateExplorer } from "@/components/workspace/StateExplorer";
import { OutputsPanel } from "@/components/workspace/OutputsPanel";
import { AccessPanel } from "@/components/workspace/AccessPanel";
import { WorkspaceSettings } from "@/components/workspace/WorkspaceSettings";
import { Pagination } from "@/components/ui/pagination";
import { useConfirm } from "@/components/ui/confirm";
import { formatRelativeTime, formatDuration, getEnvironmentColor } from "@/lib/utils";
import { navigate } from "@/hooks/useNavigate";
import { Link } from "@/components/ui/link";
import { ConfigUpload } from "@/components/workspace/ConfigUpload";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Play,
  Trash2,
  ArrowLeft,
  GitBranch,
  Upload,
  Import,
  FlaskConical,
  Clock,
  Timer,
  Settings,
  Database,
  Key,
  ListOrdered,
  Lock,
  Unlock,
  Users,
  Plus,
  X,
  FileOutput,
  Copy,
} from "lucide-react";

interface Props {
  workspaceId: string;
}

type Tab = "runs" | "variables" | "state" | "outputs" | "access" | "settings";

const validTabs: Tab[] = ["runs", "variables", "state", "outputs", "access", "settings"];

function getTabFromURL(): Tab {
  const params = new URLSearchParams(window.location.search);
  const t = params.get("tab");
  if (t && validTabs.includes(t as Tab)) return t as Tab;
  return "runs";
}

export function WorkspaceDetail({ workspaceId }: Props) {
  const queryClient = useQueryClient();
  const confirm = useConfirm();
  const [tab, setTab] = useState<Tab>(getTabFromURL);
  const [runsPage, setRunsPage] = useState(1);

  const handleTabChange = (t: Tab) => {
    setTab(t);
    const url = new URL(window.location.href);
    if (t === "runs") {
      url.searchParams.delete("tab");
    } else {
      url.searchParams.set("tab", t);
    }
    window.history.replaceState({}, "", url.toString());
  };

  const { data: workspace, isLoading: wsLoading, isError: wsError } = useQuery({
    queryKey: ["workspace", workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET("/workspaces/{workspaceId}", {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data;
    },
  });

  const { data: runsData, isLoading: runsLoading, isError: runsError } = useQuery({
    queryKey: ["runs", workspaceId, runsPage],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/workspaces/{workspaceId}/runs",
        {
          params: {
            path: { workspaceId },
            query: { page: runsPage, per_page: 20 },
          },
        }
      );
      if (error) throw error;
      return data;
    },
    enabled: tab === "runs",
    // Keep the runs list live while any run is still moving, so a plan/apply
    // kicked off here (or from the run view) advances without a manual refresh.
    refetchInterval: (query) =>
      query.state.data?.data?.some((r) => isRunInFlight(r.status)) ? 3000 : false,
  });

  const [showImport, setShowImport] = useState(false);
  const [importRows, setImportRows] = useState([{ address: "", id: "" }]);

  const createRunMutation = useMutation({
    mutationFn: async (params: { operation: RunOperation; imports?: { address: string; id: string }[] }) => {
      const { data, error } = await api.POST(
        "/workspaces/{workspaceId}/runs",
        {
          params: { path: { workspaceId } },
          body: params,
        }
      );
      if (error) throw error;
      return data;
    },
    onSuccess: (run) => {
      queryClient.invalidateQueries({ queryKey: ["runs", workspaceId] });
      setShowImport(false);
      setImportRows([{ address: "", id: "" }]);
      navigate(`/workspaces/${workspaceId}/runs/${run.id}`);
    },
    onError: () => toast.error("Failed to create run"),
  });

  const lockMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/workspaces/{workspaceId}/lock",
        { params: { path: { workspaceId } } }
      );
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace", workspaceId] });
      toast.success("Workspace locked");
    },
    onError: () => toast.error("Failed to lock workspace"),
  });

  const unlockMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/workspaces/{workspaceId}/unlock",
        { params: { path: { workspaceId } } }
      );
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace", workspaceId] });
      toast.success("Workspace unlocked");
    },
    onError: () => toast.error("Failed to unlock workspace"),
  });

  const [showCloneDialog, setShowCloneDialog] = useState(false);
  const [cloneName, setCloneName] = useState("");
  const [cloneDescription, setCloneDescription] = useState("");
  const [cloneEnvironment, setCloneEnvironment] = useState("");

  const cloneMutation = useMutation({
    mutationFn: async (body: { name: string; description?: string; environment?: string }) => {
      const { data, error } = await api.POST(
        "/workspaces/{workspaceId}/clone",
        {
          params: { path: { workspaceId } },
          body,
        }
      );
      if (error) throw error;
      return data;
    },
    onSuccess: (ws) => {
      toast.success("Workspace cloned successfully");
      window.location.href = `/workspaces/${ws.id}`;
    },
    onError: () => toast.error("Failed to clone workspace"),
  });

  const openCloneDialog = () => {
    setCloneName("");
    setCloneDescription(workspace?.description ?? "");
    setCloneEnvironment(workspace?.environment ?? "development");
    setShowCloneDialog(true);
  };

  const handleClone = () => {
    if (!cloneName.trim()) return;
    cloneMutation.mutate({
      name: cloneName.trim(),
      description: cloneDescription,
      environment: cloneEnvironment,
    });
  };

  if (wsLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Spinner className="w-6 h-6" />
      </div>
    );
  }

  if (wsError || !workspace) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center text-center">
        <p className="text-muted-foreground">{wsError ? "Failed to load workspace" : "Workspace not found"}</p>
      </div>
    );
  }

  const tabs: { id: Tab; label: string; icon: typeof ListOrdered }[] = [
    { id: "runs", label: "Runs", icon: ListOrdered },
    { id: "variables", label: "Variables", icon: Key },
    { id: "state", label: "State", icon: Database },
    { id: "outputs", label: "Outputs", icon: FileOutput },
    { id: "access", label: "Access", icon: Users },
    { id: "settings", label: "Settings", icon: Settings },
  ];

  return (
    <div className="p-6">
      {/* Header */}
      <div className="mb-6">
        <Link
          href="/"
          className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors mb-4"
        >
          <ArrowLeft className="w-3.5 h-3.5" />
          Workspaces
        </Link>

        <div className="flex items-start justify-between">
          <div>
            <div className="flex items-center gap-3 mb-2">
              <h1 className="text-lg font-semibold tracking-tight">
                {workspace.name}
              </h1>
              <Badge
                className={getEnvironmentColor(workspace.environment)}
                variant="outline"
              >
                {workspace.environment}
              </Badge>
              {workspace.locked ? (
                <Button
                  variant="outline"
                  size="sm"
                  aria-label="Unlock workspace"
                  className="text-warning border-warning/30 hover:bg-warning/10"
                  onClick={() => unlockMutation.mutate()}
                  disabled={unlockMutation.isPending}
                >
                  <Lock className="w-3.5 h-3.5" />
                  Locked
                </Button>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label="Lock workspace"
                  className="text-muted-foreground"
                  onClick={() => lockMutation.mutate()}
                  disabled={lockMutation.isPending}
                >
                  <Unlock className="w-3.5 h-3.5" />
                  Lock
                </Button>
              )}
            </div>
            {workspace.description && (
              <p className="text-muted-foreground">{workspace.description}</p>
            )}
            <div className="flex items-center gap-4 mt-3 text-sm text-muted-foreground">
              {workspace.source === "upload" ? (
                <span className="flex items-center gap-1.5">
                  <Upload className="w-4 h-4" />
                  Upload
                </span>
              ) : (
                <span className="flex items-center gap-1.5">
                  <GitBranch className="w-4 h-4" />
                  {workspace.repo_branch}
                </span>
              )}
              <span className="font-mono text-xs">
                tofu {workspace.tofu_version}
              </span>
              <span className="flex items-center gap-1.5">
                <Clock className="w-4 h-4" />
                Updated {formatRelativeTime(workspace.updated_at)}
              </span>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              onClick={openCloneDialog}
            >
              <Copy className="w-4 h-4" />
              Clone
            </Button>
            <Button
              variant="outline"
              onClick={() => createRunMutation.mutate({ operation: "test" })}
              disabled={
                createRunMutation.isPending ||
                workspace.locked ||
                (workspace.source === "upload" && !workspace.current_config_version_id)
              }
              title="Run smoke-test.sh from the working directory"
            >
              <FlaskConical className="w-4 h-4" />
              Test
            </Button>
            <Button
              variant="outline"
              onClick={() => setShowImport(true)}
              disabled={
                createRunMutation.isPending ||
                workspace.locked ||
                (workspace.source === "upload" && !workspace.current_config_version_id)
              }
            >
              <Import className="w-4 h-4" />
              Import
            </Button>
            <Button
              variant="outline"
              className="text-destructive hover:bg-destructive/10"
              onClick={async () => {
                if (
                  await confirm({
                    title: "Are you sure you want to destroy all resources?",
                    confirmLabel: "Destroy",
                  })
                ) {
                  createRunMutation.mutate({ operation: "destroy" });
                }
              }}
              disabled={
                createRunMutation.isPending ||
                workspace.locked ||
                (workspace.source === "upload" && !workspace.current_config_version_id)
              }
            >
              <Trash2 className="w-4 h-4" />
              Destroy
            </Button>
            <Button
              onClick={() => createRunMutation.mutate({ operation: "plan" })}
              disabled={
                createRunMutation.isPending ||
                workspace.locked ||
                (workspace.source === "upload" && !workspace.current_config_version_id)
              }
              title={
                workspace.locked
                  ? "Workspace is locked"
                  : workspace.source === "upload" && !workspace.current_config_version_id
                    ? "Upload configuration first"
                    : undefined
              }
            >
              {createRunMutation.isPending ? (
                <Spinner />
              ) : (
                <Play className="w-4 h-4" />
              )}
              Start plan
            </Button>
          </div>
        </div>
      </div>

      {/* Tabs */}
      <div className="border-b border-border mb-6">
        <div className="flex gap-1" role="tablist" aria-label="Workspace sections">
          {tabs.map((t) => (
            <button
              key={t.id}
              role="tab"
              aria-selected={tab === t.id}
              onClick={() => handleTabChange(t.id)}
              className={`flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 transition-colors cursor-pointer ${
                tab === t.id
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
            >
              <t.icon className="w-4 h-4" />
              {t.label}
            </button>
          ))}
        </div>
      </div>

      {/* Import dialog */}
      <Dialog open={showImport} onClose={() => { setShowImport(false); setImportRows([{ address: "", id: "" }]); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Import resources</DialogTitle>
            <DialogDescription>
              Import existing infrastructure into tofu state. Enter the resource address from your .tf files and the cloud resource ID.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 mt-2 max-h-96 overflow-auto">
            {importRows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  placeholder="module.eks.aws_eks_cluster.this[0]"
                  value={row.address}
                  onChange={(e) => {
                    const next = [...importRows];
                    next[i] = { ...next[i], address: e.target.value };
                    setImportRows(next);
                  }}
                  className="font-mono text-xs flex-1"
                />
                <Input
                  placeholder="production-eks"
                  value={row.id}
                  onChange={(e) => {
                    const next = [...importRows];
                    next[i] = { ...next[i], id: e.target.value };
                    setImportRows(next);
                  }}
                  className="font-mono text-xs flex-1"
                />
                {importRows.length > 1 && (
                  <button
                    onClick={() => setImportRows(importRows.filter((_, j) => j !== i))}
                    className="p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors cursor-pointer shrink-0"
                  >
                    <X className="w-4 h-4" />
                  </button>
                )}
              </div>
            ))}
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setImportRows([...importRows, { address: "", id: "" }])}
            >
              <Plus className="w-3.5 h-3.5" />
              Add resource
            </Button>
          </div>
          <div className="flex justify-end gap-2 mt-4">
            <Button variant="ghost" onClick={() => { setShowImport(false); setImportRows([{ address: "", id: "" }]); }}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                const valid = importRows.filter((r) => r.address.trim() && r.id.trim());
                if (valid.length === 0) { toast.error("Add at least one resource"); return; }
                createRunMutation.mutate({
                  operation: "import",
                  imports: valid.map((r) => ({ address: r.address.trim(), id: r.id.trim() })),
                });
              }}
              disabled={createRunMutation.isPending}
            >
              {createRunMutation.isPending ? <Spinner /> : <Import className="w-4 h-4" />}
              Import {importRows.filter((r) => r.address && r.id).length} resource(s)
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* Tab content */}
      {tab === "runs" && (
        <div>
          {workspace.source === "upload" && (
            <div className="mb-6">
              <h3 className="text-sm font-medium mb-3">Configuration</h3>
              <ConfigUpload
                workspaceId={workspaceId}
                currentConfigVersion={workspace.current_config_version_id}
              />
            </div>
          )}
          {runsLoading ? (
            <div className="flex items-center justify-center py-12">
              <Spinner className="w-5 h-5" />
            </div>
          ) : runsError ? (
            <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-10 text-center">
              <p className="text-sm text-destructive">Failed to load runs. Please try again.</p>
            </div>
          ) : !runsData?.data?.length ? (
            <div className="rounded-lg border border-dashed border-border p-10 text-center">
              <Play className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
              <h3 className="font-medium mb-1">No runs yet</h3>
              <p className="text-sm text-muted-foreground mb-4">
                Start a plan to see OpenTofu changes.
              </p>
              <Button
                size="sm"
                onClick={() => createRunMutation.mutate({ operation: "plan" })}
                disabled={createRunMutation.isPending}
              >
                <Play className="w-3.5 h-3.5" />
                Start plan
              </Button>
            </div>
          ) : (
            <div className="space-y-2">
              {runsData.data.map((run: Run) => (
                <Link
                  key={run.id}
                  href={`/workspaces/${workspaceId}/runs/${run.id}`}
                  className="flex items-center justify-between p-4 rounded-lg border border-border bg-card hover:border-primary/30 transition-all group"
                >
                  <div className="flex items-center gap-4">
                    <RunStatusBadge status={run.status} />
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-sm group-hover:text-primary transition-colors">
                          {run.operation.charAt(0).toUpperCase() +
                            run.operation.slice(1)}
                        </span>
                        <span className="text-xs text-muted-foreground font-mono">
                          {run.id.slice(0, 8)}
                        </span>
                      </div>
                      <div className="flex items-center gap-2 text-xs text-muted-foreground mt-0.5">
                        <span>{formatRelativeTime(run.created_at)}</span>
                        {run.started_at && (
                          <span className="flex items-center gap-1">
                            <Timer className="w-3 h-3" />
                            {formatDuration(run.started_at, run.finished_at)}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>
                  {(run.resources_added ||
                    run.resources_changed ||
                    run.resources_deleted) ? (
                    <div className="flex items-center gap-3 text-xs font-mono">
                      {run.resources_added ? (
                        <span className="text-success">
                          +{run.resources_added}
                        </span>
                      ) : null}
                      {run.resources_changed ? (
                        <span className="text-warning">
                          ~{run.resources_changed}
                        </span>
                      ) : null}
                      {run.resources_deleted ? (
                        <span className="text-destructive">
                          -{run.resources_deleted}
                        </span>
                      ) : null}
                    </div>
                  ) : null}
                </Link>
              ))}
              <Pagination
                page={runsPage}
                perPage={20}
                total={runsData.total}
                onPageChange={setRunsPage}
              />
            </div>
          )}
        </div>
      )}

      {tab === "variables" && <VariablesPanel workspaceId={workspaceId} />}
      {tab === "state" && <StateExplorer workspaceId={workspaceId} />}
      {tab === "outputs" && <OutputsPanel workspaceId={workspaceId} />}
      {tab === "access" && <AccessPanel workspaceId={workspaceId} />}
      {tab === "settings" && <WorkspaceSettings workspace={workspace} />}

      {/* Clone dialog */}
      {showCloneDialog && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-card border border-border rounded-lg shadow-lg w-full max-w-md p-6">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-semibold">Clone Workspace</h2>
              <button
                onClick={() => setShowCloneDialog(false)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="w-5 h-5" />
              </button>
            </div>
            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium mb-1">
                  Name <span className="text-destructive">*</span>
                </label>
                <input
                  type="text"
                  value={cloneName}
                  onChange={(e) => setCloneName(e.target.value)}
                  placeholder="New workspace name"
                  className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                  maxLength={128}
                  autoFocus
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Description</label>
                <textarea
                  value={cloneDescription}
                  onChange={(e) => setCloneDescription(e.target.value)}
                  rows={3}
                  className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                  maxLength={4096}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Environment</label>
                <Select
                  value={cloneEnvironment}
                  onChange={(e) => setCloneEnvironment(e.target.value)}
                >
                  <option value="development">development</option>
                  <option value="staging">staging</option>
                  <option value="production">production</option>
                </Select>
              </div>
              <div className="flex justify-end gap-2 pt-2">
                <Button
                  variant="outline"
                  onClick={() => setShowCloneDialog(false)}
                >
                  Cancel
                </Button>
                <Button
                  onClick={handleClone}
                  disabled={!cloneName.trim() || cloneMutation.isPending}
                >
                  {cloneMutation.isPending ? <Spinner /> : <Copy className="w-4 h-4" />}
                  Clone workspace
                </Button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
