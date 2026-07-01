import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Approval, RunStatus } from "@/api/models";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { formatRelativeTime } from "@/lib/utils";
import { Check, X, MessageSquare, ShieldCheck } from "lucide-react";

interface Props {
  workspaceId: string;
  runId: string;
  runStatus: RunStatus;
}

export function ApprovalPanel({ workspaceId, runId, runStatus }: Props) {
  const queryClient = useQueryClient();
  const [comment, setComment] = useState("");

  const needsApproval =
    runStatus === "planned" || runStatus === "awaiting_approval";

  const { data: approvals, isLoading } = useQuery({
    queryKey: ["approvals", runId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/workspaces/{workspaceId}/runs/{runId}/approvals",
        {
          params: { path: { workspaceId, runId } },
        }
      );
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const approveMutation = useMutation({
    mutationFn: async (status: "approved" | "rejected") => {
      const { data, error } = await api.POST(
        "/workspaces/{workspaceId}/runs/{runId}/approvals",
        {
          params: { path: { workspaceId, runId } },
          body: { status, comment },
        }
      );
      if (error) throw error;
      return data;
    },
    onSuccess: (_data, status) => {
      queryClient.invalidateQueries({ queryKey: ["approvals", runId] });
      queryClient.invalidateQueries({ queryKey: ["run", runId] });
      setComment("");
      toast.success(status === "approved" ? "Run approved" : "Run rejected");
    },
    onError: () => toast.error("Failed to submit approval"),
  });

  return (
    <div className="border-t border-border p-4">
      <div className="flex items-center gap-2 mb-3">
        <ShieldCheck className="w-4 h-4 text-muted-foreground" />
        <h3 className="text-sm font-semibold">Approvals</h3>
      </div>

      {/* Existing approvals */}
      {isLoading ? (
        <Spinner className="w-4 h-4" />
      ) : approvals?.length ? (
        <div className="space-y-2 mb-3">
          {(approvals as Approval[]).map((a) => (
            <div
              key={a.id}
              className="flex items-start gap-2 text-sm p-2 rounded bg-accent/30"
            >
              {a.avatar_url ? (
                <img
                  src={a.avatar_url}
                  alt=""
                  className="w-5 h-5 rounded-full mt-0.5"
                />
              ) : (
                <div className="w-5 h-5 rounded-full bg-primary/20 flex items-center justify-center text-[10px] mt-0.5">
                  {(a.user_name || "?")[0]}
                </div>
              )}
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{a.user_name || "User"}</span>
                  <span
                    className={
                      a.status === "approved"
                        ? "text-success text-xs"
                        : "text-destructive text-xs"
                    }
                  >
                    {a.status === "approved" ? "approved" : "rejected"}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {formatRelativeTime(a.created_at)}
                  </span>
                </div>
                {a.comment && (
                  <p className="text-muted-foreground mt-0.5">{a.comment}</p>
                )}
              </div>
            </div>
          ))}
        </div>
      ) : null}

      {/* Approval actions */}
      {needsApproval && (
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <MessageSquare className="w-3.5 h-3.5 text-muted-foreground" />
            <Input
              placeholder="Add a comment (optional)"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              className="flex-1"
            />
          </div>
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              onClick={() => approveMutation.mutate("approved")}
              disabled={approveMutation.isPending}
              className="bg-success hover:bg-success/90 text-success-foreground"
            >
              {approveMutation.isPending ? (
                <Spinner />
              ) : (
                <Check className="w-3.5 h-3.5" />
              )}
              Approve & Apply
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => approveMutation.mutate("rejected")}
              disabled={approveMutation.isPending}
              className="text-destructive hover:bg-destructive/10"
            >
              <X className="w-3.5 h-3.5" />
              Reject
            </Button>
          </div>
        </div>
      )}

      {!needsApproval && !approvals?.length && (
        <p className="text-sm text-muted-foreground">No approvals required.</p>
      )}
    </div>
  );
}
