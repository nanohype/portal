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
// watcher runs, so locally the timeline honestly rests at "committed". The tofu
// build's CURRENT error (if any) rides on the tofu_running phase's detail and is
// shown inline while building — it's regressible (a transient provider error
// clears next tick), so it is NOT a terminal "Failed". A definitive Failed is
// reserved for a portal-side commit failure (op.status / the "failed" phase).
export function VendTimeline({ op }: { op: ClusterOperation }) {
  const phases = op.vend_phases ?? {};
  const failed = op.status === "failed" || "failed" in phases;
  const tofuErr = phases.tofu_running?.detail;

  const reached = (key: string): boolean => {
    if (key === "queued") return true;
    if (key === "active") return op.status === "active" || "active" in phases;
    return key in phases;
  };

  let lastReached = 0;
  STEPS.forEach((s, i) => {
    if (reached(s.key)) lastReached = i;
  });
  const allDone = !failed && reached("active");
  const inFlight = !failed && !allDone;
  const failIndex = Math.min(lastReached + 1, STEPS.length - 1);
  // While building, a current tofu error surfaces inline (amber-ish via
  // destructive) without ending the timeline.
  const issue = inFlight && !!tofuErr;
  const label = failed
    ? "Failed"
    : issue
      ? tofuErr || ""
      : STEPS[lastReached].label;
  const tooltip =
    (failed ? op.error || phases.failed?.detail : tofuErr) || undefined;

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
          "text-[11px] truncate max-w-[18rem]",
          failed || issue ? "text-destructive" : "text-muted-foreground",
        )}
      >
        {label}
      </span>
    </div>
  );
}
