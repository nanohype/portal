import type { ClusterOperation } from "@/api/types";
import { PhaseStepper } from "./PhaseStepper";
import { timelineFrame, type Step } from "./timeline-frame";

const STEPS: readonly Step[] = [
  { key: "queued", label: "Queued" },
  { key: "committed", label: "Committed" },
  { key: "deprovisioning", label: "Destroying" },
  { key: "deprovisioned", label: "Removed" },
];

// Compact stepper for a deprovision teardown — the mirror of VendTimeline. After
// the CR is removed (committed), ArgoCD prunes and Crossplane runs tofu destroy;
// the in-cluster watcher advances "deprovisioning" → "deprovisioned" as the
// Cluster XR disappears from the hub. Locally (watcher off) it rests at
// committed. A tofu-destroy error rides regressibly on the deprovisioning phase
// and is shown inline without ending the timeline; a definitive Failed is the
// portal-side commit failure only.
export function DeprovisionTimeline({ op }: { op: ClusterOperation }) {
  const phases = op.vend_phases ?? {};
  const failed = op.status === "failed" || "failed" in phases;
  const done = op.status === "deprovisioned" || "deprovisioned" in phases;
  const frame = timelineFrame(
    STEPS,
    (key) => {
      if (key === "queued") return true;
      // committed is implied once teardown finished, even if its phase row is absent.
      if (key === "committed") return op.status === "committed" || done || "committed" in phases;
      if (key === "deprovisioned") return done;
      return key in phases;
    },
    failed,
  );

  const destroyErr = phases.deprovisioning?.detail;
  const issue = frame.inFlight && !!destroyErr;
  const label = failed
    ? "Failed"
    : issue
      ? destroyErr || ""
      : STEPS[frame.lastReached].label;
  const tooltip =
    (failed ? op.error || phases.failed?.detail : destroyErr) || undefined;

  return (
    <PhaseStepper
      steps={STEPS}
      frame={frame}
      failed={failed}
      label={label}
      tooltip={tooltip}
      alert={failed || issue}
    />
  );
}
