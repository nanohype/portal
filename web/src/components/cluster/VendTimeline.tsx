import type { ClusterOperation } from "@/api/types";
import { PhaseStepper } from "./PhaseStepper";
import { timelineFrame, type Step } from "./timeline-frame";

const STEPS: readonly Step[] = [
  { key: "queued", label: "Queued" },
  { key: "committed", label: "Committed" },
  { key: "tofu_running", label: "Building" },
  { key: "active", label: "Active" },
];

// Compact stepper projecting a provision vend's journey from its phase map.
// Substrate phases (tofu_running, active) only advance once the in-cluster
// watcher runs, so locally the timeline honestly rests at "committed". The tofu
// build's CURRENT error (if any) rides on the tofu_running phase's detail and is
// shown inline while building — it's regressible (a transient provider error
// clears next tick), so it is NOT a terminal "Failed". A definitive Failed is
// reserved for a portal-side commit failure (op.status / the "failed" phase).
export function VendTimeline({ op }: { op: ClusterOperation }) {
  const phases = op.vend_phases ?? {};
  const failed = op.status === "failed" || "failed" in phases;
  const frame = timelineFrame(
    STEPS,
    (key) => {
      if (key === "queued") return true;
      if (key === "active") return op.status === "active" || "active" in phases;
      return key in phases;
    },
    failed,
  );

  const tofuErr = phases.tofu_running?.detail;
  const issue = frame.inFlight && !!tofuErr;
  const label = failed
    ? "Failed"
    : issue
      ? tofuErr || ""
      : STEPS[frame.lastReached].label;
  const tooltip =
    (failed ? op.error || phases.failed?.detail : tofuErr) || undefined;

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
