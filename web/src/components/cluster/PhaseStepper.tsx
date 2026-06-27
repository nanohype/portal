import { cn } from "@/lib/utils";
import type { Step, Frame } from "./timeline-frame";

// PhaseStepper is the shared presentational dots+label stepper. The caller
// computes the frame and the cosmetic label/tooltip/alert from the op; this just
// draws filled/frontier/fail dots and the trailing label.
export function PhaseStepper({
  steps,
  frame,
  failed,
  label,
  tooltip,
  alert,
}: {
  steps: readonly Step[];
  frame: Frame;
  failed: boolean;
  label: string;
  tooltip?: string;
  alert: boolean;
}) {
  const { lastReached, inFlight, failIndex } = frame;
  return (
    <div className="flex items-center gap-2" title={tooltip}>
      <div className="flex items-center">
        {steps.map((s, i) => {
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
              {i < steps.length - 1 && (
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
          alert ? "text-destructive" : "text-muted-foreground",
        )}
      >
        {label}
      </span>
    </div>
  );
}
