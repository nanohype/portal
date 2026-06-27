import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { StatusBadge } from "@/components/ui/status-badge";
import { tenantOpStatus } from "@/lib/status";
import { Spinner } from "@/components/ui/spinner";
import { formatRelativeTime } from "@/lib/utils";
import type { Cluster, ClusterOperation, OpsFeedItem } from "@/api/types";
import { Cloud, Layers, Activity } from "lucide-react";
import { VendTimeline } from "@/components/cluster/VendTimeline";
import { DeprovisionTimeline } from "@/components/cluster/DeprovisionTimeline";


// A cluster op is still moving if pending, or a committed provision/deprovision
// that hasn't reached a terminal and isn't a portal-side failure (and is younger
// than an hour). Mirrors ClusterList's orders-poll predicate.
function clusterOpInFlight(o: ClusterOperation): boolean {
  if (o.status === "pending") return true;
  if (o.status === "active" || o.status === "failed" || o.status === "deprovisioned")
    return false;
  const p = o.vend_phases ?? {};
  if ("failed" in p) return false;
  return Date.now() - new Date(o.created_at).getTime() < 60 * 60 * 1000;
}

function itemInFlight(item: OpsFeedItem): boolean {
  if (item.kind === "cluster" && item.cluster) return clusterOpInFlight(item.cluster);
  if (item.kind === "tenant" && item.tenant) return item.tenant.status === "pending";
  return false;
}

export function OpsPage() {
  const {
    data: feed,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ["ops-feed"],
    queryFn: async () => {
      const { data, error } = await api.GET("/ops/feed");
      if (error) throw error;
      return data?.data ?? [];
    },
    // Poll while anything across the org is still moving — the page is the only
    // thing polling, so it stops on nav.
    refetchInterval: (query) =>
      query.state.data?.some(itemInFlight) ? 3000 : false,
  });

  // cluster_id → name for tenant rows. Distinct ["clusters","list"] key returning
  // the bare array (the accounts-lookup convention) — avoids the envelope-shape
  // collision with ClusterList's ["clusters"] query.
  const { data: clusters } = useQuery({
    queryKey: ["clusters", "list"],
    queryFn: async () => {
      const { data, error } = await api.GET("/clusters", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });
  const clusterName = (id: string) =>
    clusters?.find((c: Cluster) => c.id === id)?.name ?? id;

  const items = feed ?? [];

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="mb-6">
        <h1 className="text-lg font-semibold tracking-tight">Operations</h1>
        <p className="text-[12px] text-muted-foreground mt-1">
          Recent activity across cluster vends and tenant deploys
        </p>
      </div>

      {isLoading ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <Spinner className="w-6 h-6 text-primary" />
        </div>
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
            Failed to load the operations feed.
          </div>
        </div>
      ) : items.length === 0 ? (
        <div className="flex-1 flex flex-col items-center justify-center text-center animate-fade-up">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <Activity className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No operations yet</h2>
          <p className="text-xs text-muted-foreground max-w-[320px]">
            Provision a cluster or deploy a tenant and it shows up here the moment
            it&apos;s placed.
          </p>
        </div>
      ) : (
        <div className="space-y-1.5">
          {items.map((item, i) =>
            item.kind === "cluster" && item.cluster ? (
              <OpRow
                key={`cluster-${item.cluster.id}`}
                delay={i}
                icon={<Cloud className="w-3.5 h-3.5 text-primary/60 shrink-0" />}
                title={item.cluster.name}
                subtitle={`${item.cluster.environment} · ${item.cluster.operation}`}
                middle={
                  item.cluster.operation === "provision" ? (
                    <VendTimeline op={item.cluster} />
                  ) : (
                    <DeprovisionTimeline op={item.cluster} />
                  )
                }
                sha={item.cluster.git_commit_sha}
                at={item.at}
                error={
                  item.cluster.status === "failed" ? item.cluster.error : ""
                }
              />
            ) : item.kind === "tenant" && item.tenant ? (
              <OpRow
                key={`tenant-${item.tenant.id}`}
                delay={i}
                icon={<Layers className="w-3.5 h-3.5 text-primary/60 shrink-0" />}
                title={item.tenant.tenant_name}
                subtitle={`on ${clusterName(item.tenant.cluster_id)} · ${item.tenant.operation}`}
                middle={<StatusBadge visual={tenantOpStatus(item.tenant.status)} />}
                sha={item.tenant.git_commit_sha}
                at={item.at}
                error={item.tenant.status === "failed" ? item.tenant.error : ""}
              />
            ) : null,
          )}
        </div>
      )}
    </div>
  );
}

function OpRow({
  icon,
  title,
  subtitle,
  middle,
  sha,
  at,
  error,
  delay,
}: {
  icon: React.ReactNode;
  title: string;
  subtitle: string;
  middle: React.ReactNode;
  sha: string;
  at: string;
  error: string;
  delay: number;
}) {
  return (
    <div
      className="border border-border/60 rounded-lg px-4 py-2.5 animate-fade-up"
      style={{ animationDelay: `${delay * 25}ms` }}
    >
      <div className="flex items-center gap-3 text-sm">
        {icon}
        <span className="font-medium">{title}</span>
        <span className="text-[11px] text-muted-foreground/70">{subtitle}</span>
        {middle}
        <div className="ml-auto flex items-center gap-3 text-[11px] text-muted-foreground/70">
          {sha && <span className="font-mono">{sha.slice(0, 7)}</span>}
          <span>{formatRelativeTime(at)}</span>
        </div>
      </div>
      {error && (
        <p className="text-[11px] text-destructive/90 mt-1.5 font-mono break-all">
          {error}
        </p>
      )}
    </div>
  );
}
