import type { ClusterOperation } from "@/api/types";
import { cn } from "@/lib/utils";

const STEPS = [
  { key: "queued", label: "Queued" },
  { key: "committed", label: "Committed" },
  { key: "tofu_running", label: "Building" },
  { key: "active", label: "Active" },
] as const;

// Compact stepper projecting a provision vend's journey from its phase map.
// Substrate phases (tofu_running, active) only advance once the in-cluster
// watcher is running, so locally the timeline honestly rests at "committed" —
// the later dots stay dim rather than faking progress. This is a projection of
// the substrate, not a re-derived verdict.
export function VendTimeline({ op }: { op: ClusterOperation }) {
  const phases = op.vend_phases ?? {};
  const failed =
    op.status === "failed" || "failed" in phases || "tofu_failed" in phases;

  const reached = (key: string): boolean => {
    if (key === "queued") return true;
    if (key === "active") return op.status === "active" || "active" in phases;
    return key in phases;
  };

  // Frontier index: the furthest step reached. The active transition can land
  // (op.status='active') before any tofu_running phase is recorded, so gate the
  // dots on `i <= lastReached` rather than each step's own reached() — otherwise
  // a reached "Active" would sit past a hollow "Building".
  let lastReached = 0;
  STEPS.forEach((s, i) => {
    if (reached(s.key)) lastReached = i;
  });
  const allDone = !failed && reached("active");
  const inFlight = !failed && !allDone;
  // Clamp so a regression past the last step still paints a red dot.
  const failIndex = Math.min(lastReached + 1, STEPS.length - 1);
  const label = failed ? "Failed" : STEPS[lastReached].label;
  const tooltip = failed
    ? op.error || phases.tofu_failed?.detail || phases.failed?.detail || undefined
    : undefined;

  return (
    <div className="flex items-center gap-2" title={tooltip}>
      <div className="flex items-center">
        {STEPS.map((s, i) => {
          const isDone = i <= lastReached;
          const isFrontier = inFlight && i === lastReached;
          const isFailPoint = failed && i === failIndex;
          return (
            <div key={s.key} className="flex items-center">
              <span
                className={cn(
                  "w-2 h-2 rounded-full shrink-0",
                  isFailPoint
                    ? "bg-destructive"
                    : isDone
                      ? cn("bg-primary", isFrontier && "animate-pulse")
                      : "border border-border/70",
                )}
              />
              {i < STEPS.length - 1 && (
                <span
                  className={cn(
                    "w-3 h-px",
                    i < lastReached ? "bg-primary/40" : "bg-border/50",
                  )}
                />
              )}
            </div>
          );
        })}
      </div>
      <span
        className={cn(
          "text-[11px]",
          failed ? "text-destructive" : "text-muted-foreground",
        )}
      >
        {label}
      </span>
    </div>
  );
}
