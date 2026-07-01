import type { Cluster, ClusterOperation } from "@/api/models";

// A cluster's rolled-up state for the fleet verdict. "" health columns mean
// "not observed" (no ArgoCD Application yet, EKS describe not permitted) — that
// is NOT a problem, so it never counts as attention.
export type ClusterState = "attention" | "transitional" | "healthy";

export function clusterState(c: Cluster): ClusterState {
  if (
    c.connection_status === "failed" ||
    c.argocd_health_status === "Degraded" ||
    c.argocd_health_status === "Missing" ||
    c.control_plane_status === "DEGRADED" ||
    c.control_plane_status === "FAILED"
  ) {
    return "attention";
  }
  if (
    c.connection_status === "connecting" ||
    c.connection_status === "pending" ||
    c.argocd_health_status === "Progressing" ||
    c.control_plane_status === "UPDATING" ||
    c.control_plane_status === "CREATING"
  ) {
    return "transitional";
  }
  return "healthy";
}

// A vend/deprovision op is still moving while pending or committed; active /
// deprovisioned / failed are terminal.
function inFlight(op: ClusterOperation): boolean {
  return op.status === "pending" || op.status === "committed";
}

export interface FleetSummary {
  total: number;
  attention: number;
  transitional: number;
  healthy: number;
  inFlightVends: number;
  inFlightDeprovisions: number;
  versionSpread: { version: string; count: number }[];
  byEnvironment: { environment: string; count: number }[];
  /** true when no cluster needs attention. */
  green: boolean;
}

function tally(values: string[]): { key: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const v of values) counts.set(v, (counts.get(v) ?? 0) + 1);
  return [...counts.entries()]
    .map(([key, count]) => ({ key, count }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
}

export function summarizeFleet(
  clusters: Cluster[],
  operations: ClusterOperation[],
): FleetSummary {
  const states = clusters.map(clusterState);
  const attention = states.filter((s) => s === "attention").length;
  const transitional = states.filter((s) => s === "transitional").length;

  return {
    total: clusters.length,
    attention,
    transitional,
    healthy: states.filter((s) => s === "healthy").length,
    inFlightVends: operations.filter(
      (o) => o.operation === "provision" && inFlight(o),
    ).length,
    inFlightDeprovisions: operations.filter(
      (o) => o.operation === "deprovision" && inFlight(o),
    ).length,
    versionSpread: tally(clusters.map((c) => c.k8s_version || "unknown")).map(
      ({ key, count }) => ({ version: key, count }),
    ),
    byEnvironment: tally(clusters.map((c) => c.environment)).map(
      ({ key, count }) => ({ environment: key, count }),
    ),
    green: attention === 0,
  };
}
