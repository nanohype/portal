import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { PipelineRunStage } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusBadge } from "@/components/ui/status-badge";
import {
  isPipelineRunInFlight,
  pipelineRunStatus,
  pipelineStageStatus,
} from "@/lib/status";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatDuration } from "@/lib/utils";
import {
  ArrowLeft,
  CheckCircle2,
  XCircle,
  Clock,
  Ban,
  Loader2,
  Import,
  Pause,
  SkipForward,
  ExternalLink,
  Check,
  X,
} from "lucide-react";

function stageStatusIcon(status: string) {
  const base = "w-[18px] h-[18px]";
  switch (status) {
    case "completed":
      return <CheckCircle2 className={`${base} text-success`} />;
    case "errored":
      return <XCircle className={`${base} text-destructive`} />;
    case "running":
      return <Loader2 className={`${base} text-primary animate-spin`} />;
    case "importing_outputs":
      return <Import className={`${base} text-primary animate-pulse`} />;
    case "awaiting_approval":
      return <Pause className={`${base} text-warning`} />;
    case "cancelled":
      return <Ban className={`${base} text-muted-foreground/60`} />;
    case "skipped":
      return <SkipForward className={`${base} text-muted-foreground/60`} />;
    default:
      return <Clock className={`${base} text-muted-foreground/40`} />;
  }
}

function progressBarColor(status: string) {
  switch (status) {
    case "completed":
      return "bg-success";
    case "running":
    case "importing_outputs":
      return "bg-primary animate-shimmer";
    case "errored":
      return "bg-destructive";
    case "awaiting_approval":
      return "bg-warning";
    case "cancelled":
      return "bg-muted-foreground/20";
    default:
      return "bg-border/40";
  }
}

function stageBorderStyle(status: string) {
  switch (status) {
    case "running":
    case "importing_outputs":
      return "border-primary/30 bg-primary/[0.03]";
    case "completed":
      return "border-success/20";
    case "errored":
      return "border-destructive/20";
    case "awaiting_approval":
      return "border-warning/20";
    default:
      return "border-border/50";
  }
}

export function PipelineRunView({
  pipelineId,
  runId,
}: {
  pipelineId: string;
  runId: string;
}) {
  const queryClient = useQueryClient();

  const { data, isLoading, isError } = useQuery({
    queryKey: ["pipeline-run", pipelineId, runId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/pipelines/{pipelineId}/runs/{runId}",
        { params: { path: { pipelineId, runId } } }
      );
      if (error) throw error;
      return data!;
    },
    refetchInterval: (query) => {
      const pr = query.state.data?.pipeline_run;
      return pr && isPipelineRunInFlight(pr.status) ? 3000 : false;
    },
  });

  const cancelMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/pipelines/{pipelineId}/runs/{runId}/cancel",
        { params: { path: { pipelineId, runId } } }
      );
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["pipeline-run", pipelineId, runId],
      });
      toast.success("Pipeline run cancelled");
    },
    onError: () => toast.error("Failed to cancel pipeline run"),
  });

  // Inline approve/reject for stages parked in awaiting_approval. Hits the
  // same /workspaces/{ws}/runs/{run}/approvals endpoint ApprovalPanel uses.
  const approveMutation = useMutation({
    mutationFn: async ({
      workspaceId,
      stageRunId,
      status,
    }: {
      workspaceId: string;
      stageRunId: string;
      status: "approved" | "rejected";
    }) => {
      const { data, error } = await api.POST(
        "/workspaces/{workspaceId}/runs/{runId}/approvals",
        {
          params: { path: { workspaceId, runId: stageRunId } },
          body: { status, comment: "" },
        }
      );
      if (error) throw error;
      return data;
    },
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({
        queryKey: ["pipeline-run", pipelineId, runId],
      });
      toast.success(
        vars.status === "approved" ? "Stage approved" : "Stage rejected"
      );
    },
    onError: () => toast.error("Failed to submit approval"),
  });

  if (isLoading) {
    return (
      <div className="flex justify-center py-20">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="p-6">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load pipeline run.
        </div>
      </div>
    );
  }

  const { pipeline_run: pr, stages } = data;
  const isRunning = pr.status === "running";

  return (
    <div className="p-6 animate-fade-up">
      {/* Header */}
      <div className="mb-6">
        <Link
          href={`/pipelines/${pipelineId}`}
          className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 mb-3 transition-colors"
        >
          <ArrowLeft className="w-3 h-3" />
          Pipeline
        </Link>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <h1 className="text-lg font-semibold tracking-tight">
              Pipeline Run
            </h1>
            <StatusBadge visual={pipelineRunStatus(pr.status)} />
          </div>
          <div className="flex items-center gap-3">
            <span className="text-xs text-muted-foreground font-mono">
              {formatDuration(pr.started_at, pr.finished_at)}
            </span>
            {isRunning && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => cancelMutation.mutate()}
                disabled={cancelMutation.isPending}
                className="text-destructive hover:text-destructive hover:bg-destructive/10 hover:border-destructive/30"
              >
                <Ban className="w-3 h-3" />
                Cancel
              </Button>
            )}
          </div>
        </div>

        <div className="text-[11px] text-muted-foreground/70 mt-1.5 flex items-center gap-1.5">
          <span>
            Stage {pr.current_stage + 1} of {pr.total_stages}
          </span>
          <span className="text-border">|</span>
          <span>Started {new Date(pr.started_at).toLocaleString()}</span>
          {pr.finished_at && (
            <>
              <span className="text-border">|</span>
              <span>
                Finished {new Date(pr.finished_at).toLocaleString()}
              </span>
            </>
          )}
        </div>
      </div>

      {/* Progress bar */}
      <div className="mb-8">
        <div className="flex gap-1 h-1.5 rounded-full overflow-hidden bg-border/20">
          {stages.map((stage: PipelineRunStage) => (
            <div
              key={stage.id}
              className={`flex-1 rounded-full transition-all duration-500 ${progressBarColor(stage.status)}`}
            />
          ))}
        </div>
      </div>

      {/* Stage cards */}
      <div className="relative">
        {/* Vertical connector */}
        {stages.length > 1 && (
          <div
            className="absolute left-[19px] top-[40px] w-px bg-border/40"
            style={{ height: `calc(100% - 56px)` }}
          />
        )}
        <div className="space-y-2">
          {stages.map((stage: PipelineRunStage, i: number) => {
            const isActive =
              stage.status === "running" ||
              stage.status === "importing_outputs";
            const isAwaitingApproval =
              stage.status === "awaiting_approval" && !!stage.run_id;
            const isPending =
              approveMutation.isPending &&
              approveMutation.variables?.stageRunId === stage.run_id;
            return (
              <div
                key={stage.id}
                className="relative flex items-start gap-3 animate-fade-up"
                style={{ animationDelay: `${i * 50}ms` }}
              >
                {/* Status dot */}
                <div
                  className={`w-10 h-10 rounded-full border-2 bg-card flex items-center justify-center z-10 shrink-0 transition-all duration-300 ${
                    stage.status === "completed"
                      ? "border-success/40"
                      : isActive
                      ? "border-primary/50"
                      : stage.status === "errored"
                      ? "border-destructive/40"
                      : "border-border/50"
                  }`}
                >
                  {stageStatusIcon(stage.status)}
                </div>

                {/* Card */}
                <div
                  className={`flex-1 border rounded-lg px-4 py-3 transition-all duration-200 ${stageBorderStyle(stage.status)} ${isActive ? "animate-glow-pulse" : ""}`}
                >
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium">
                          {stage.workspace_name}
                        </span>
                        <StatusBadge visual={pipelineStageStatus(stage.status)} />
                      </div>
                      {stage.started_at && (
                        <div className="text-[11px] text-muted-foreground/60 mt-1 font-mono">
                          {formatDuration(stage.started_at, stage.finished_at)}
                        </div>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <Badge
                        variant={stage.auto_apply ? "success" : "secondary"}
                      >
                        {stage.auto_apply ? "auto" : "manual"}
                      </Badge>
                      {isAwaitingApproval && (
                        <>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() =>
                              approveMutation.mutate({
                                workspaceId: stage.workspace_id,
                                stageRunId: stage.run_id!,
                                status: "approved",
                              })
                            }
                            disabled={isPending}
                            className="text-success hover:text-success hover:bg-success/10 hover:border-success/40 h-7 px-2"
                          >
                            <Check className="w-3 h-3" />
                            Approve
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() =>
                              approveMutation.mutate({
                                workspaceId: stage.workspace_id,
                                stageRunId: stage.run_id!,
                                status: "rejected",
                              })
                            }
                            disabled={isPending}
                            className="text-destructive hover:text-destructive hover:bg-destructive/10 hover:border-destructive/40 h-7 px-2"
                          >
                            <X className="w-3 h-3" />
                            Reject
                          </Button>
                        </>
                      )}
                      {stage.run_id && (
                        <Link
                          href={`/workspaces/${stage.workspace_id}/runs/${stage.run_id}`}
                          className="inline-flex items-center gap-1 text-[11px] text-primary/80 hover:text-primary transition-colors"
                        >
                          View Run
                          <ExternalLink className="w-2.5 h-2.5" />
                        </Link>
                      )}
                    </div>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
