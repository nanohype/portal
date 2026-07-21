import { describe, it, expect } from 'vitest';
import { roleAtLeast } from './roles';

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
