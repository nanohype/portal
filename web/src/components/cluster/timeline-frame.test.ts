import { describe, it, expect } from 'vitest';
import { timelineFrame, type Step } from './timeline-frame';

const STEPS: Step[] = [
  { key: 'queued', label: 'Queued' },
  { key: 'planning', label: 'Planning' },
  { key: 'applying', label: 'Applying' },
  { key: 'done', label: 'Done' },
];

const reached = (keys: string[]) => (key: string) => keys.includes(key);

describe('timelineFrame', () => {
  it('reads as nothing-filled when no step is reached', () => {
    const f = timelineFrame(STEPS, () => false, false);
    expect(f.lastReached).toBe(-1);
    expect(f.inFlight).toBe(true);
    expect(f.failIndex).toBe(0); // min(-1 + 1, len - 1)
  });

  it('tracks the frontier and stays in-flight mid-run', () => {
    const f = timelineFrame(STEPS, reached(['queued', 'planning']), false);
    expect(f.lastReached).toBe(1);
    expect(f.inFlight).toBe(true);
    expect(f.failIndex).toBe(2);
  });

  it('is done (not in-flight) when the last step is reached', () => {
    const f = timelineFrame(STEPS, () => true, false);
    expect(f.lastReached).toBe(3);
    expect(f.inFlight).toBe(false);
    expect(f.failIndex).toBe(3); // clamped to len - 1
  });

  it('is not in-flight when failed; the fail dot lands on the next step', () => {
    const f = timelineFrame(STEPS, reached(['queued', 'planning']), true);
    expect(f.inFlight).toBe(false);
    expect(f.failIndex).toBe(2);
  });

  it('clamps failIndex to the last step when failing on the final step', () => {
    const f = timelineFrame(STEPS, () => true, true);
    expect(f.lastReached).toBe(3);
    expect(f.failIndex).toBe(3); // min(4, 3)
  });

  it('lastReached is the highest reached index, even past a gap', () => {
    const f = timelineFrame(STEPS, reached(['queued', 'applying']), false);
    expect(f.lastReached).toBe(2);
  });
});
