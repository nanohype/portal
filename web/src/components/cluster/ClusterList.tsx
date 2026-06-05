import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import type { Account, Cluster, ClusterConnectionStatus } from "@/api/types";
import { Server, Plus, Cloud } from "lucide-react";
import { ClusterCreateModal } from "./ClusterCreateModal";
import { ClusterOrderModal } from "./ClusterOrderModal";

export function statusBadge(status: ClusterConnectionStatus) {
  switch (status) {
    case "connected":
      return <Badge variant="success">Connected</Badge>;
    case "connecting":
      return <Badge variant="default">Connecting</Badge>;
    case "failed":
      return <Badge variant="destructive">Failed</Badge>;
    default:
      return <Badge variant="secondary">Pending</Badge>;
  }
}

export function ClusterList() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";
  const [showCreate, setShowCreate] = useState(false);
  const [showOrder, setShowOrder] = useState(false);

  const { data, isLoading, isError } = useQuery({
    queryKey: ["clusters"],
    queryFn: async () => {
      const { data, error } = await api.GET("/clusters", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data!;
    },
    // Re-poll while any cluster is still in a transitional state so the UI
    // reflects the async connection-test result without a manual refresh.
    refetchInterval: (query) => {
      const clusters = query.state.data?.data;
      if (!clusters) return false;
      const transitional = clusters.some(
        (c: Cluster) =>
          c.connection_status === "pending" ||
          c.connection_status === "connecting",
      );
      return transitional ? 3000 : false;
    },
  });

  const { data: accounts } = useQuery({
    queryKey: ["accounts"],
    queryFn: async () => {
      const { data, error } = await api.GET("/accounts", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data!.data;
    },
  });

  if (isLoading) {
    return (
      <div className="flex justify-center py-20">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="p-6">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load clusters.
        </div>
      </div>
    );
  }

  const clusters = data?.data ?? [];
  const accountName = (id: string) =>
    accounts?.find((a: Account) => a.id === id)?.name ?? id;

  return (
    <div className="p-6">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Clusters</h1>
          <p className="text-[12px] text-muted-foreground mt-1">
            Kubernetes clusters portal watches
          </p>
        </div>
        {isAdmin && (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setShowCreate(true)}
              disabled={!accounts || accounts.length === 0}
            >
              <Plus className="w-3.5 h-3.5" />
              Register
            </Button>
            <Button
              size="sm"
              onClick={() => setShowOrder(true)}
              disabled={!accounts || accounts.length === 0}
            >
              <Cloud className="w-3.5 h-3.5" />
              Provision
            </Button>
          </div>
        )}
      </div>

      {clusters.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-20 text-center animate-fade-up">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <Server className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No clusters yet</h2>
          <p className="text-xs text-muted-foreground mb-5 max-w-[320px]">
            {!accounts || accounts.length === 0
              ? "Add an Account first — every cluster belongs to one."
              : "Register a Kubernetes cluster so portal can watch its workloads."}
          </p>
          {isAdmin && accounts && accounts.length > 0 && (
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="w-3.5 h-3.5" />
              Add Cluster
            </Button>
          )}
          {(!accounts || accounts.length === 0) && (
            <Link
              href="/accounts"
              className="text-xs text-primary hover:underline"
            >
              Go to Accounts
            </Link>
          )}
        </div>
      ) : (
        <div className="space-y-2">
          {clusters.map((c, i) => (
            <Link
              key={c.id}
              href={`/clusters/${c.id}`}
              className="group block border border-border/60 rounded-lg px-4 py-3.5 hover:bg-accent/30 hover:border-primary/15 transition-all duration-150 animate-fade-up"
              style={{ animationDelay: `${i * 30}ms` }}
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg bg-primary/8 flex items-center justify-center shrink-0">
                    <Server className="w-3.5 h-3.5 text-primary/70" />
                  </div>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium group-hover:text-primary transition-colors">
                        {c.name}
                      </span>
                      {statusBadge(c.connection_status)}
                    </div>
                    <p className="text-[11px] text-muted-foreground/70 mt-0.5">
                      {accountName(c.account_id)} · {c.region}
                      {c.k8s_version && ` · ${c.k8s_version}`}
                    </p>
                  </div>
                </div>
                <div className="flex items-center gap-3 shrink-0 text-[11px] text-muted-foreground/70">
                  {c.node_count > 0 && (
                    <span>
                      {c.node_count} node{c.node_count === 1 ? "" : "s"}
                    </span>
                  )}
                  {c.last_connected_at && (
                    <span>{formatRelativeTime(c.last_connected_at)}</span>
                  )}
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}

      <ClusterCreateModal
        open={showCreate}
        onClose={() => setShowCreate(false)}
        accounts={accounts ?? []}
      />

      <ClusterOrderModal
        open={showOrder}
        onClose={() => setShowOrder(false)}
        accounts={accounts ?? []}
      />
    </div>
  );
}
