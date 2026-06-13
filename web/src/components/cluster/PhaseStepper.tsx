import { cn } from "@/lib/utils";

export interface Step {
  key: string;
  label: string;
}

export interface Frame {
  lastReached: number;
  inFlight: boolean;
  failIndex: number;
}

// timelineFrame is the pure projection an op's phases make onto a step list: how
// far the dots fill (lastReached, monotonic), whether work is still moving
// (inFlight), and where a fail dot lands if it failed (failIndex = the step it
// couldn't finish). Kept pure + separate from rendering so each op kind
// (provision, deprovision) supplies its own `reached` rule and shares the look.
export function timelineFrame(
  steps: readonly Step[],
  reached: (key: string) => boolean,
  failed: boolean,
): Frame {
  // -1, not 0: a step is only "reached" when its predicate says so. Callers make
  // the first step (queued) unconditionally reached, so in practice this lands at
  // 0+, but the shared helper must not assume that — an all-unreached frame must
  // read as nothing filled, not step 0 filled.
  let lastReached = -1;
  steps.forEach((s, i) => {
    if (reached(s.key)) lastReached = i;
  });
  const allDone = !failed && reached(steps[steps.length - 1].key);
  const inFlight = !failed && !allDone;
  return { lastReached, inFlight, failIndex: Math.min(lastReached + 1, steps.length - 1) };
}

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
