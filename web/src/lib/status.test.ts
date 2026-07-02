import { describe, it, expect } from 'vitest';
import {
  isRunInFlight,
  isPipelineRunInFlight,
  isTenantPhaseTransitional,
  controlPlane,
  argo,
  tenantPhase,
  tenantOpStatus,
  runStatus,
  pipelineRunStatus,
} from './status';

describe('isRunInFlight', () => {
  it('is true for moving or may-move-without-action states', () => {
    for (const s of [
      'pending',
      'queued',
      'planning',
      'planned',
      'awaiting_approval',
      'applying',
    ] as const) {
      expect(isRunInFlight(s)).toBe(true);
    }
  });

  it('is false for terminal states', () => {
    for (const s of ['applied', 'errored', 'cancelled', 'discarded'] as const) {
      expect(isRunInFlight(s)).toBe(false);
    }
  });
});

describe('isPipelineRunInFlight', () => {
  it('is true for idle and running', () => {
    expect(isPipelineRunInFlight('idle')).toBe(true);
    expect(isPipelineRunInFlight('running')).toBe(true);
  });

  it('is false for terminal states', () => {
    for (const s of ['completed', 'errored', 'cancelled'] as const) {
      expect(isPipelineRunInFlight(s)).toBe(false);
    }
  });
});

describe('isTenantPhaseTransitional', () => {
  it('is true for pending/provisioning, case-insensitive', () => {
    expect(isTenantPhaseTransitional('pending')).toBe(true);
    expect(isTenantPhaseTransitional('Provisioning')).toBe(true);
    expect(isTenantPhaseTransitional('PROVISIONING')).toBe(true);
  });

  it('is false for at-rest phases and unknowns', () => {
    for (const p of ['ready', 'healthy', 'degraded', 'failed', '', 'weird']) {
      expect(isTenantPhaseTransitional(p)).toBe(false);
    }
  });
});

describe('controlPlane', () => {
  it('returns null when unobserved', () => {
    expect(controlPlane('')).toBeNull();
  });

  it('maps ACTIVE to success with a title-cased label', () => {
    const v = controlPlane('ACTIVE');
    expect(v?.variant).toBe('success');
    expect(v?.label).toBe('Active');
  });

  it('maps in-progress states to a spinning warning', () => {
    for (const s of ['UPDATING', 'CREATING']) {
      const v = controlPlane(s);
      expect(v?.variant).toBe('warning');
      expect(v?.spinning).toBe(true);
    }
  });

  it('maps DEGRADED/FAILED to destructive', () => {
    expect(controlPlane('DEGRADED')?.variant).toBe('destructive');
    expect(controlPlane('FAILED')?.variant).toBe('destructive');
  });

  it('falls back to secondary for unknown states', () => {
    const v = controlPlane('SOMETHING');
    expect(v?.variant).toBe('secondary');
    expect(v?.label).toBe('Something');
  });
});

describe('argo', () => {
  it('returns null when neither sync nor health is observed', () => {
    expect(argo('', '')).toBeNull();
  });

  it('is success and joins the label when Healthy', () => {
    const v = argo('Synced', 'Healthy');
    expect(v?.variant).toBe('success');
    expect(v?.label).toBe('Synced · Healthy');
  });

  it('is a spinning warning when Progressing', () => {
    const v = argo('Synced', 'Progressing');
    expect(v?.variant).toBe('warning');
    expect(v?.spinning).toBe(true);
  });

  it('is destructive when Degraded or Missing', () => {
    expect(argo('Synced', 'Degraded')?.variant).toBe('destructive');
    expect(argo('Synced', 'Missing')?.variant).toBe('destructive');
  });

  it('warns on OutOfSync even when health is unobserved', () => {
    expect(argo('OutOfSync', '')?.variant).toBe('warning');
  });
});

describe('tenantPhase', () => {
  it('maps ready-family phases to success, case-insensitively', () => {
    for (const p of ['ready', 'Active', 'HEALTHY']) {
      expect(tenantPhase(p).variant).toBe('success');
    }
  });

  it('spins on pending/provisioning', () => {
    expect(tenantPhase('Provisioning').spinning).toBe(true);
  });

  it('is destructive on error-family phases', () => {
    for (const p of ['error', 'Failed', 'DEGRADED']) {
      expect(tenantPhase(p).variant).toBe('destructive');
    }
  });

  it('labels an empty phase Unknown', () => {
    expect(tenantPhase('').label).toBe('Unknown');
  });
});

describe('tenantOpStatus', () => {
  it('maps committed and failed', () => {
    expect(tenantOpStatus('committed').label).toBe('Committed');
    expect(tenantOpStatus('failed').variant).toBe('destructive');
  });

  it('treats anything else as Pending', () => {
    expect(tenantOpStatus('whatever').label).toBe('Pending');
  });
});

describe('status record lookups', () => {
  it('resolves a known run status visual', () => {
    expect(runStatus.applied.variant).toBe('success');
  });

  it('pipelineRunStatus returns the mapped visual', () => {
    expect(pipelineRunStatus('running').spinning).toBe(true);
  });
});
