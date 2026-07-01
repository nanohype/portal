import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Link } from "@/components/ui/link";
import { Spinner } from "@/components/ui/spinner";
import { summarizeFleet } from "@/lib/fleet";
import type { ClusterOperation } from "@/api/models";
import { CheckCircle2, AlertTriangle, Server, Activity } from "lucide-react";

function opMoving(o: ClusterOperation): boolean {
  return o.status === "pending" || o.status === "committed";
}

export function FleetOverview() {
  // Reuse the bare-array ["clusters","list"] key (the ops/accounts convention)
  // — distinct from ClusterList's enveloped ["clusters"] so the shapes can't
  // collide in the query cache.
  const { data: clusters, isLoading } = useQuery({
    queryKey: ["clusters", "list"],
    queryFn: async () => {
      const { data, error } = await api.GET("/clusters", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const { data: operations } = useQuery({
    queryKey: ["cluster-operations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/cluster-orders");
      if (error) throw error;
      return data?.data ?? [];
    },
    refetchInterval: (query) =>
      (query.state.data ?? []).some(opMoving) ? 5000 : false,
  });

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  const s = summarizeFleet(clusters ?? [], operations ?? []);
  const inFlight = s.inFlightVends + s.inFlightDeprovisions;

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="mb-6">
        <h1 className="text-lg font-semibold tracking-tight">Fleet</h1>
        <p className="text-[12px] text-muted-foreground mt-1">
          Every cluster the org runs, at a glance
        </p>
      </div>

      {/* Verdict */}
      <div
        className={`rounded-lg border p-4 mb-6 flex items-center gap-3 ${
          s.green
            ? "bg-success/8 border-success/20"
            : "bg-warning/10 border-warning/25"
        }`}
      >
        {s.green ? (
          <CheckCircle2 className="w-5 h-5 text-success shrink-0" />
        ) : (
          <AlertTriangle className="w-5 h-5 text-warning shrink-0" />
        )}
        <div className="text-sm font-medium">
          {s.total === 0
            ? "No clusters yet"
            : s.green
              ? `All ${s.total} cluster${s.total === 1 ? "" : "s"} healthy`
              : `${s.attention} of ${s.total} cluster${
                  s.total === 1 ? "" : "s"
                } need attention`}
        </div>
      </div>

      {/* Stat tiles */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-6">
        <Stat label="Clusters" value={s.total} icon={Server} />
        <Stat label="Healthy" value={s.healthy} tone="success" />
        <Stat label="Need attention" value={s.attention} tone="warning" />
        <Stat label="In flight" value={inFlight} icon={Activity} />
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 mb-6">
        <Breakdown
          title="Kubernetes versions"
          rows={s.versionSpread.map((v) => ({ key: v.version, count: v.count }))}
          empty="No clusters"
        />
        <Breakdown
          title="By environment"
          rows={s.byEnvironment.map((e) => ({
            key: e.environment,
            count: e.count,
          }))}
          empty="No clusters"
        />
      </div>

      {(s.inFlightVends > 0 || s.inFlightDeprovisions > 0) && (
        <p className="text-[12px] text-muted-foreground mb-4">
          {s.inFlightVends} vend{s.inFlightVends === 1 ? "" : "s"} ·{" "}
          {s.inFlightDeprovisions} teardown
          {s.inFlightDeprovisions === 1 ? "" : "s"} in flight —{" "}
          <Link href="/ops" className="text-primary hover:underline">
            watch in Operations
          </Link>
        </p>
      )}

      <Link href="/clusters" className="text-[13px] text-primary hover:underline">
        View all clusters →
      </Link>
    </div>
  );
}

function Stat({
  label,
  value,
  icon: Icon,
  tone,
}: {
  label: string;
  value: number;
  icon?: typeof Server;
  tone?: "success" | "warning";
}) {
  const valueColor =
    tone === "success" && value > 0
      ? "text-success"
      : tone === "warning" && value > 0
        ? "text-warning"
        : "text-foreground";
  return (
    <div className="rounded-lg border border-border bg-card/40 p-3">
      <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground mb-1">
        {Icon && <Icon className="w-3 h-3" />}
        {label}
      </div>
      <div className={`text-2xl font-semibold tabular-nums ${valueColor}`}>
        {value}
      </div>
    </div>
  );
}

function Breakdown({
  title,
  rows,
  empty,
}: {
  title: string;
  rows: { key: string; count: number }[];
  empty: string;
}) {
  return (
    <div className="rounded-lg border border-border bg-card/40 p-3">
      <div className="text-[11px] text-muted-foreground mb-2">{title}</div>
      {rows.length === 0 ? (
        <div className="text-xs text-muted-foreground/70">{empty}</div>
      ) : (
        <div className="space-y-1">
          {rows.map((r) => (
            <div
              key={r.key}
              className="flex items-center justify-between text-[13px]"
            >
              <span className="font-mono text-muted-foreground">{r.key}</span>
              <span className="tabular-nums">{r.count}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
