import { describe, it, expect } from 'vitest';
import { roleAtLeast, minRole } from './roles';

describe('roleAtLeast', () => {
  it('clears a bar the role meets or beats', () => {
    expect(roleAtLeast('viewer', 'viewer')).toBe(true);
    expect(roleAtLeast('operator', 'viewer')).toBe(true);
    expect(roleAtLeast('admin', 'operator')).toBe(true);
    expect(roleAtLeast('owner', 'admin')).toBe(true);
  });

  it('refuses a bar above the role', () => {
    expect(roleAtLeast('viewer', 'operator')).toBe(false);
    expect(roleAtLeast('operator', 'admin')).toBe(false);
    expect(roleAtLeast('admin', 'owner')).toBe(false);
  });

  it('treats an unknown or absent role as no authority', () => {
    expect(roleAtLeast(undefined, 'viewer')).toBe(false);
    expect(roleAtLeast(null, 'viewer')).toBe(false);
    expect(roleAtLeast('', 'viewer')).toBe(false);
    expect(roleAtLeast('superadmin', 'viewer')).toBe(false);
  });
});

// A workspace grant names a team; a membership names what someone is inside
// that team. What they actually hold is the lower of the two, which is why a
// team granted operator hands viewer to its viewers. The Access panel shows
// this per member so a capped grant is visible instead of failing silently at
// the API.
describe('minRole', () => {
  it('returns the lower of the two roles', () => {
    expect(minRole('operator', 'viewer')).toBe('viewer');
    expect(minRole('viewer', 'operator')).toBe('viewer');
    expect(minRole('admin', 'operator')).toBe('operator');
    expect(minRole('owner', 'admin')).toBe('admin');
  });

  it('returns the shared role when both sides agree', () => {
    expect(minRole('operator', 'operator')).toBe('operator');
    expect(minRole('owner', 'owner')).toBe('owner');
  });

  it('yields nothing when either side is unknown or absent', () => {
    expect(minRole('admin', undefined)).toBe('');
    expect(minRole(null, 'admin')).toBe('');
    expect(minRole('admin', 'superadmin')).toBe('');
    expect(minRole('', '')).toBe('');
  });
});
