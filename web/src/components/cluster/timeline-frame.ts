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
