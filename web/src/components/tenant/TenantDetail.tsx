import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { navigate } from "@/hooks/useNavigate";
import type { Team, TenantOperation, TenantTeamAccess } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import { StatusBadge } from "@/components/ui/status-badge";
import {
  tenantPhase,
  tenantOpStatus,
  isTenantPhaseTransitional,
} from "@/lib/status";
import {
  ArrowLeft,
  Boxes,
  ChevronDown,
  ChevronRight,
  Trash2,
} from "lucide-react";

export function TenantDetail({ tenantId }: { tenantId: string }) {
  const { user } = useAuth();
  const canWrite =
    user?.role === "operator" ||
    user?.role === "admin" ||
    user?.role === "owner";
  const queryClient = useQueryClient();

  const { data, isLoading, isError } = useQuery({
    queryKey: ["tenant", tenantId],
    queryFn: async () => {
      const { data, error } = await api.GET("/tenants/{tenantId}", {
        params: { path: { tenantId } },
      });
      if (error) throw error;
      return data!;
    },
    // Poll fast only while the tenant is still settling; otherwise back off.
    refetchInterval: (query) =>
      query.state.data && isTenantPhaseTransitional(query.state.data.phase)
        ? 5000
        : 30000,
  });

  const { data: operations } = useQuery({
    queryKey: ["tenant", tenantId, "operations"],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/tenants/{tenantId}/operations",
        { params: { path: { tenantId } } },
      );
      if (error) throw error;
      return data?.data ?? [];
    },
    // Operations refresh more aggressively than the tenant itself — the
    // worker transitions a row pending→committed in under a second after
    // enqueue, and we want the UI to reflect that.
    refetchInterval: (q) => {
      const ops = q.state.data as TenantOperation[] | undefined;
      const transitional = ops?.some((o) => o.status === "pending");
      return transitional ? 2000 : 10000;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.DELETE("/tenants/{tenantId}", {
        params: { path: { tenantId } },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tenant", tenantId, "operations"] });
      queryClient.invalidateQueries({ queryKey: ["tenants"] });
      toast.success("Tenant delete enqueued · ArgoCD will prune shortly");
      navigate("/tenants");
    },
    onError: () => toast.error("Failed to enqueue tenant delete"),
  });

  const [showSpec, setShowSpec] = useState(false);
  const [showStatus, setShowStatus] = useState(true);

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load tenant.
        </div>
      </div>
    );
  }

  return (
    <div className="p-6 w-full max-w-3xl mx-auto flex-1 flex flex-col animate-fade-up">
      <Link
        href="/tenants"
        className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 mb-3 transition-colors"
      >
        <ArrowLeft className="w-3 h-3" />
        Tenants
      </Link>

      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-primary/8 flex items-center justify-center">
            <Boxes className="w-4 h-4 text-primary/70" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-lg font-semibold tracking-tight">
                {data.name}
              </h1>
              <StatusBadge visual={tenantPhase(data.phase)} />
            </div>
            <p className="text-[12px] text-muted-foreground mt-0.5">
              <Link
                href={`/clusters/${data.cluster_id}`}
                className="hover:text-foreground"
              >
                cluster {data.cluster_id}
              </Link>
              {" · "}observed {formatRelativeTime(data.last_observed_at)}
            </p>
          </div>
        </div>
        {canWrite && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              if (
                confirm(`Delete tenant "${data.name}"? This commits a removal to the tenants repo and ArgoCD will prune the resources.`)
              ) {
                deleteMutation.mutate();
              }
            }}
            disabled={deleteMutation.isPending}
            className="text-destructive hover:text-destructive hover:bg-destructive/10 hover:border-destructive/30"
          >
            <Trash2 className="w-3 h-3" />
            Delete tenant
          </Button>
        )}
      </div>

      {operations && operations.length > 0 && (
        <OperationsPanel ops={operations} />
      )}

      {canWrite && <AccessPanel tenantId={tenantId} />}

      <JSONPanel
        title="Status"
        open={showStatus}
        onToggle={() => setShowStatus(!showStatus)}
        value={data.status}
      />

      <JSONPanel
        title="Spec"
        open={showSpec}
        onToggle={() => setShowSpec(!showSpec)}
        value={data.spec}
      />
    </div>
  );
}

function AccessPanel({ tenantId }: { tenantId: string }) {
  const queryClient = useQueryClient();
  const [picker, setPicker] = useState("");

  const { data: access } = useQuery({
    queryKey: ["tenant", tenantId, "access"],
    queryFn: async () => {
      const { data, error } = await api.GET("/tenants/{tenantId}/access", {
        params: { path: { tenantId } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const { data: teams } = useQuery({
    queryKey: ["teams", "all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/teams");
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const grant = useMutation({
    mutationFn: async (teamID: string) => {
      const { data, error } = await api.POST(
        "/tenants/{tenantId}/access",
        { params: { path: { tenantId } }, body: { team_id: teamID } },
      );
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tenant", tenantId, "access"] });
      toast.success("Access granted");
      setPicker("");
    },
    onError: () => toast.error("Failed to grant access"),
  });

  const revoke = useMutation({
    mutationFn: async (teamID: string) => {
      const { error } = await api.DELETE(
        "/tenants/{tenantId}/access/{teamId}",
        { params: { path: { tenantId, teamId: teamID } } },
      );
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tenant", tenantId, "access"] });
      toast.success("Access revoked");
    },
    onError: () => toast.error("Failed to revoke access"),
  });

  const grantedIDs = new Set((access ?? []).map((a: TenantTeamAccess) => a.team_id));
  const ungranted = (teams ?? []).filter((t: Team) => !grantedIDs.has(t.id));
  const teamName = (id: string) =>
    (teams ?? []).find((t: Team) => t.id === id)?.name ?? id;

  return (
    <div className="mb-6 border border-border/60 rounded-lg overflow-hidden">
      <div className="px-4 py-2 text-xs font-medium border-b border-border/40 bg-accent/20 flex items-center justify-between">
        <span>Team Access ({access?.length ?? 0})</span>
      </div>
      <div className="divide-y divide-border/30">
        {(access ?? []).length === 0 ? (
          <div className="px-4 py-3 text-xs text-muted-foreground">
            No teams have access yet — admins can see this tenant; operators
            and viewers can only see it after a grant.
          </div>
        ) : (
          (access ?? []).map((a: TenantTeamAccess) => (
            <div
              key={a.id}
              className="px-4 py-2.5 text-xs flex items-center gap-3"
            >
              <span className="font-medium flex-1">{teamName(a.team_id)}</span>
              <span className="text-muted-foreground/70">
                {formatRelativeTime(a.granted_at)}
              </span>
              <button
                onClick={() => {
                  if (confirm(`Revoke access from ${teamName(a.team_id)}?`)) {
                    revoke.mutate(a.team_id);
                  }
                }}
                className="text-muted-foreground hover:text-destructive transition-colors cursor-pointer"
                aria-label="Revoke"
              >
                <Trash2 className="w-3 h-3" />
              </button>
            </div>
          ))
        )}
        {ungranted.length > 0 && (
          <div className="px-4 py-2 bg-background/40 flex items-center gap-2">
            <select
              value={picker}
              onChange={(e) => setPicker(e.target.value)}
              className="text-xs border border-border/60 rounded-md bg-background px-2 py-1.5 flex-1"
            >
              <option value="">Grant access to a team…</option>
              {ungranted.map((t: Team) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
            <Button
              size="sm"
              onClick={() => picker && grant.mutate(picker)}
              disabled={!picker || grant.isPending}
            >
              Grant
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function OperationsPanel({ ops }: { ops: TenantOperation[] }) {
  return (
    <div className="mb-6 border border-border/60 rounded-lg overflow-hidden">
      <div className="px-4 py-2 text-xs font-medium border-b border-border/40 bg-accent/20">
        Operations ({ops.length})
      </div>
      <div className="divide-y divide-border/30">
        {ops.map((op) => (
          <div key={op.id} className="px-4 py-2.5 text-xs flex items-center gap-3">
            <StatusBadge visual={tenantOpStatus(op.status)} />
            <span className="font-mono text-[11px] uppercase tracking-wide text-muted-foreground">
              {op.operation}
            </span>
            <span className="text-muted-foreground/70 flex-1">
              {formatRelativeTime(op.created_at)}
            </span>
            {op.git_commit_sha && (
              <code className="text-[10px] text-muted-foreground/70 font-mono">
                {op.git_commit_sha.slice(0, 8)}
              </code>
            )}
            {op.error && (
              <span className="text-[11px] text-destructive truncate max-w-[40%]" title={op.error}>
                {op.error}
              </span>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function JSONPanel({
  title,
  open,
  onToggle,
  value,
}: {
  title: string;
  open: boolean;
  onToggle: () => void;
  value: unknown;
}) {
  return (
    <div className="mb-4 border border-border/60 rounded-lg overflow-hidden">
      <button
        type="button"
        onClick={onToggle}
        className="w-full flex items-center gap-2 px-4 py-2.5 text-xs font-medium hover:bg-accent/30 transition-colors cursor-pointer"
      >
        {open ? (
          <ChevronDown className="w-3 h-3" />
        ) : (
          <ChevronRight className="w-3 h-3" />
        )}
        {title}
      </button>
      {open && (
        <pre className="px-4 py-3 text-[11px] font-mono whitespace-pre-wrap break-words bg-background/40 border-t border-border/40 max-h-[500px] overflow-auto">
          {JSON.stringify(value, null, 2)}
        </pre>
      )}
    </div>
  );
}
