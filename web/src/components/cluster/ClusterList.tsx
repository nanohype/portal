import { useState, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import type { Account, Cluster, ClusterConnectionStatus, ClusterOperation } from "@/api/types";
import { Server, Plus, Cloud } from "lucide-react";
import { ClusterCreateModal } from "./ClusterCreateModal";
import { ClusterOrderModal } from "./ClusterOrderModal";
import { VendTimeline } from "./VendTimeline";

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

// Status of a vend ORDER (the cluster_operations row) — distinct from the
// connection status of a registered cluster above.
export function operationBadge(status: string) {
  switch (status) {
    case "active":
      return <Badge variant="success">Active</Badge>;
    case "committed":
      return <Badge variant="default">Committed</Badge>;
    case "failed":
      return <Badge variant="destructive">Failed</Badge>;
    case "pending":
      return <Badge variant="secondary">Pending</Badge>;
    default:
      return <Badge variant="secondary">{status}</Badge>;
  }
}

export function ClusterList() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";
  const [showCreate, setShowCreate] = useState(false);
  const [showOrder, setShowOrder] = useState(false);
  const [opsPage, setOpsPage] = useState(1);

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

  // Distinct key from AccountList's ["accounts"] (which caches the paginated
  // envelope, not the bare array): a shared key let the envelope shape reach
  // this page's accounts.find()/.length and crash on rapid navigation. The
  // ["accounts"] prefix still matches account invalidations.
  const { data: accounts } = useQuery({
    queryKey: ["accounts", "list"],
    queryFn: async () => {
      const { data, error } = await api.GET("/accounts", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  // Vend ORDERS (cluster_operations) — surfaces in-flight + recent provisions so
  // an order is visible the moment it's placed, even before it registers as a
  // connected cluster. Polls while the worker is still committing one.
  const { data: operations } = useQuery({
    queryKey: ["cluster-operations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/cluster-orders");
      if (error) throw error;
      return data!;
    },
    // Poll while anything is still moving: a pending op, or a committed provision
    // still working toward active. The in-cluster watcher advances tofu_running →
    // active live; locally they rest at committed (and the page is the only thing
    // polling, so it stops on nav). A tofu error is regressible — it rides on
    // tofu_running and clears, so it does NOT stop the poll; only a portal-side
    // "failed" phase does. Cap at an hour so a vend that never reaches active
    // (watcher off, order abandoned) can't poll forever.
    refetchInterval: (query) =>
      query.state.data?.some((o: ClusterOperation) => {
        if (o.status === "pending") return true;
        if (o.operation !== "provision" || o.status === "active" || o.status === "failed")
          return false;
        const p = o.vend_phases ?? {};
        if ("failed" in p) return false;
        const ageMs = Date.now() - new Date(o.created_at).getTime();
        return ageMs < 60 * 60 * 1000;
      })
        ? 3000
        : false,
  });

  // Clamp the orders page if the list ever shrinks under it — defensive, so a
  // page transition can never strand the user on an empty trailing page.
  const maxOpsPage = Math.max(1, Math.ceil((operations?.length ?? 0) / 5));
  useEffect(() => {
    if (opsPage > maxOpsPage) setOpsPage(maxOpsPage);
  }, [maxOpsPage, opsPage]);

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
    <div className="p-6 flex flex-col flex-1">
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
        <div className="flex-1 flex flex-col items-center justify-center text-center animate-fade-up">
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

      {operations && operations.length > 0 && (
        <div className="mt-8 pt-6 border-t border-border/60">
          <h2 className="text-[11px] font-medium uppercase tracking-wider text-dim mb-2">
            Recent orders
          </h2>
          <div className="space-y-1.5">
            {operations.slice((opsPage - 1) * 5, opsPage * 5).map((op) => (
              <div
                key={op.id}
                className="border border-border/60 rounded-lg px-4 py-2.5"
              >
                <div className="flex items-center gap-3 text-sm">
                  <Cloud className="w-3.5 h-3.5 text-primary/60 shrink-0" />
                  <span className="font-medium">{op.name}</span>
                  <span className="text-[11px] text-muted-foreground/70">
                    {op.environment} · {op.operation}
                  </span>
                  {op.operation === "provision" ? (
                    <VendTimeline op={op} />
                  ) : (
                    operationBadge(op.status)
                  )}
                  <div className="ml-auto flex items-center gap-3 text-[11px] text-muted-foreground/70">
                    {op.git_commit_sha && (
                      <span className="font-mono">{op.git_commit_sha.slice(0, 7)}</span>
                    )}
                    <span>{formatRelativeTime(op.created_at)}</span>
                  </div>
                </div>
                {op.status === "failed" && op.error && (
                  <p className="text-[11px] text-destructive/90 mt-1.5 font-mono break-all">
                    {op.error}
                  </p>
                )}
              </div>
            ))}
          </div>
          {operations.length > 5 && (
            <div className="flex items-center justify-between mt-3 text-[11px] text-muted-foreground/70">
              <button
                onClick={() => setOpsPage((p) => Math.max(1, p - 1))}
                disabled={opsPage === 1}
                className="hover:text-foreground disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
              >
                Previous
              </button>
              <span>
                {(opsPage - 1) * 5 + 1}–{Math.min(opsPage * 5, operations.length)} of{" "}
                {operations.length}
              </span>
              <button
                onClick={() =>
                  setOpsPage((p) => (p * 5 < operations.length ? p + 1 : p))
                }
                disabled={opsPage * 5 >= operations.length}
                className="hover:text-foreground disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
              >
                Next
              </button>
            </div>
          )}
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
