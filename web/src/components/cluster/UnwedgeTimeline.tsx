import type { ClusterOperation } from '@/api/models';
import { PhaseStepper } from './PhaseStepper';
import { timelineFrame, type Step } from './timeline-frame';

const STEPS: readonly Step[] = [
  { key: 'queued', label: 'Queued' },
  { key: 'verified', label: 'Verified' },
  { key: 'tearing-down', label: 'Tearing down' },
  { key: 'torn-down', label: 'Torn down' },
  { key: 'deprovisioned', label: 'Released' },
];

// Compact stepper for a break-glass unwedge. The worker advances the phases it
// writes — verified (the Workspace is genuinely wedged) → tearing-down (deleting
// the spoke's tagged AWS resources) → torn-down (count) → released (finalizers
// dropped, op marked deprovisioned). A fatal teardown surfaces as Failed; the
// torn-down detail carries the deleted-resource count for reassurance.
export function UnwedgeTimeline({ op }: { op: ClusterOperation }) {
  const phases = op.vend_phases ?? {};
  const failed = op.status === 'failed' || 'failed' in phases;
  const done = op.status === 'deprovisioned' || 'deprovisioned' in phases;
  const frame = timelineFrame(
    STEPS,
    (key) => {
      if (key === 'queued') return true;
      if (key === 'deprovisioned') return done;
      return key in phases;
    },
    failed,
  );

  const progress = phases['torn-down']?.detail || phases['tearing-down']?.detail;
  const label = failed ? 'Failed' : STEPS[frame.lastReached].label;
  const tooltip = (failed ? op.error || phases.failed?.detail : progress) || undefined;

  return (
    <PhaseStepper
      steps={STEPS}
      frame={frame}
      failed={failed}
      label={label}
      tooltip={tooltip}
      alert={failed}
    />
  );
}
