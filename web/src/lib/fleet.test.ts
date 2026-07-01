import { describe, it, expect } from "vitest";
import { clusterState, summarizeFleet } from "./fleet";
import type { Cluster, ClusterOperation } from "@/api/models";

function cluster(p: Partial<Cluster>): Cluster {
  return {
    connection_status: "connected",
    argocd_health_status: "",
    control_plane_status: "",
    k8s_version: "1.30",
    environment: "production",
    ...p,
  } as Cluster;
}

function op(p: Partial<ClusterOperation>): ClusterOperation {
  return {
    operation: "provision",
    status: "active",
    ...p,
  } as ClusterOperation;
}

describe("clusterState", () => {
  it("is healthy when connected and nothing degraded (unobserved health is fine)", () => {
    expect(clusterState(cluster({}))).toBe("healthy");
  });

  it("is attention on a failed connection or degraded ArgoCD/control-plane", () => {
    expect(clusterState(cluster({ connection_status: "failed" }))).toBe(
      "attention",
    );
    expect(clusterState(cluster({ argocd_health_status: "Degraded" }))).toBe(
      "attention",
    );
    expect(clusterState(cluster({ argocd_health_status: "Missing" }))).toBe(
      "attention",
    );
    expect(clusterState(cluster({ control_plane_status: "FAILED" }))).toBe(
      "attention",
    );
  });

  it("is transitional while connecting / progressing / updating", () => {
    expect(clusterState(cluster({ connection_status: "connecting" }))).toBe(
      "transitional",
    );
    expect(clusterState(cluster({ argocd_health_status: "Progressing" }))).toBe(
      "transitional",
    );
    expect(clusterState(cluster({ control_plane_status: "UPDATING" }))).toBe(
      "transitional",
    );
  });

  it("ranks attention over transitional when both apply", () => {
    expect(
      clusterState(
        cluster({ connection_status: "failed", control_plane_status: "UPDATING" }),
      ),
    ).toBe("attention");
  });
});

describe("summarizeFleet", () => {
  it("counts states and goes green only when nothing needs attention", () => {
    const s = summarizeFleet(
      [
        cluster({}),
        cluster({ control_plane_status: "UPDATING" }),
        cluster({ connection_status: "failed" }),
      ],
      [],
    );
    expect(s.total).toBe(3);
    expect(s.healthy).toBe(1);
    expect(s.transitional).toBe(1);
    expect(s.attention).toBe(1);
    expect(s.green).toBe(false);
  });

  it("is green for an all-healthy fleet", () => {
    expect(summarizeFleet([cluster({}), cluster({})], []).green).toBe(true);
  });

  it("counts only in-flight vends and deprovisions", () => {
    const s = summarizeFleet(
      [],
      [
        op({ operation: "provision", status: "committed" }),
        op({ operation: "provision", status: "active" }), // terminal
        op({ operation: "deprovision", status: "pending" }),
        op({ operation: "deprovision", status: "deprovisioned" }), // terminal
      ],
    );
    expect(s.inFlightVends).toBe(1);
    expect(s.inFlightDeprovisions).toBe(1);
  });

  it("tallies the version spread, bucketing empty as unknown, busiest first", () => {
    const s = summarizeFleet(
      [
        cluster({ k8s_version: "1.30" }),
        cluster({ k8s_version: "1.30" }),
        cluster({ k8s_version: "1.29" }),
        cluster({ k8s_version: "" }),
      ],
      [],
    );
    expect(s.versionSpread).toEqual([
      { version: "1.30", count: 2 },
      { version: "1.29", count: 1 },
      { version: "unknown", count: 1 },
    ]);
  });
});
