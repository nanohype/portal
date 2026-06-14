import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import type { Cluster, Tenant } from "@/api/types";
import { Button } from "@/components/ui/button";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import { Boxes, Plus } from "lucide-react";
import { TenantCreateModal } from "./TenantCreateModal";
import { StatusBadge } from "@/components/ui/status-badge";
import { tenantPhase } from "@/lib/status";

export function TenantList() {
  const { user } = useAuth();
  const canWrite =
    user?.role === "operator" ||
    user?.role === "admin" ||
    user?.role === "owner";
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, isError } = useQuery({
    queryKey: ["tenants"],
    queryFn: async () => {
      const { data, error } = await api.GET("/tenants", {
        params: { query: { per_page: 200 } },
      });
      if (error) throw error;
      return data!;
    },
    // Cheap-ish: 10s refresh while viewing the list so newly-observed tenants
    // appear without a manual refresh. The watcher reconciles every 60s
    // backend-side, so this just keeps the UI in step.
    refetchInterval: 10000,
  });

  // Distinct key from ClusterList's ["clusters"] (which caches the paginated
  // envelope, not the bare array): a shared key let one page's shape poison the
  // other's render and crash on rapid navigation. The ["clusters"] prefix still
  // matches cluster invalidations, so this stays fresh on mutations.
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

  const tenants = data?.data ?? [];
  const clusterName = (id: string) =>
    clusters?.find((c: Cluster) => c.id === id)?.name ?? id;

  // Group tenants by cluster so the list reads cluster-first
  const grouped = tenants.reduce<Record<string, Tenant[]>>((acc, t) => {
    (acc[t.cluster_id] ??= []).push(t);
    return acc;
  }, {});

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Tenants</h1>
          <p className="text-[12px] text-muted-foreground mt-1">
            eks-agent-platform tenants discovered by portal's cluster watcher (refreshed
            every 60s)
          </p>
        </div>
        {canWrite && (
          <Button
            size="sm"
            onClick={() => setShowCreate(true)}
            disabled={!clusters || clusters.length === 0}
          >
            <Plus className="w-3.5 h-3.5" />
            New Tenant
          </Button>
        )}
      </div>

      {isLoading ? (
        <SkeletonRows />
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
            Failed to load tenants.
          </div>
        </div>
      ) : tenants.length === 0 ? (
        <div className="flex-1 flex flex-col items-center justify-center text-center animate-fade-up">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <Boxes className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No tenants yet</h2>
          <p className="text-xs text-muted-foreground max-w-[320px]">
            None of the connected clusters report any eks-agent-platform Tenant CRs. The
            watcher will pick them up automatically once they exist.
          </p>
        </div>
      ) : (
        <div className="space-y-6">
          {Object.entries(grouped).map(([cid, items]) => (
            <div key={cid}>
              <div className="flex items-center gap-2 mb-2 text-xs font-mono uppercase tracking-wider text-muted-foreground">
                <span>{clusterName(cid)}</span>
                <span className="text-border">·</span>
                <span>{items.length}</span>
              </div>
              <div className="space-y-2">
                {items.map((t, i) => (
                  <Link
                    key={t.id}
                    href={`/tenants/${t.id}`}
                    className="group block border border-border/60 rounded-lg px-4 py-3 hover:bg-accent/30 hover:border-primary/15 transition-all duration-150 animate-fade-up"
                    style={{ animationDelay: `${i * 20}ms` }}
                  >
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-3 min-w-0">
                        <div className="w-7 h-7 rounded-lg bg-primary/8 flex items-center justify-center shrink-0">
                          <Boxes className="w-3 h-3 text-primary/70" />
                        </div>
                        <div className="min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium group-hover:text-primary transition-colors">
                              {t.name}
                            </span>
                            <StatusBadge visual={tenantPhase(t.phase)} />
                          </div>
                        </div>
                      </div>
                      <span className="text-[11px] text-muted-foreground/70 shrink-0">
                        observed {formatRelativeTime(t.last_observed_at)}
                      </span>
                    </div>
                  </Link>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      <TenantCreateModal
        open={showCreate}
        onClose={() => setShowCreate(false)}
        clusters={clusters ?? []}
      />
    </div>
  );
}
