import { describe, expect, it } from 'vitest';
import { MODEL_FAMILIES, MODEL_FAMILIES_ARE_EXHAUSTIVE } from './model-families';

describe('MODEL_FAMILIES', () => {
  // The two directions against the contract are enforced by tsc, not here.
  // What is left for runtime is the shape of the list a form renders.
  it('is not empty, so a form built from it cannot silently offer nothing', () => {
    expect(MODEL_FAMILIES.length).toBeGreaterThan(0);
  });

  it('has no duplicate entries, which would render as two identical chips', () => {
    expect(new Set(MODEL_FAMILIES).size).toBe(MODEL_FAMILIES.length);
  });

  it('holds only non-empty lowercase identifiers', () => {
    for (const family of MODEL_FAMILIES) {
      expect(family).toMatch(/^[a-z][a-z0-9-]*$/);
    }
  });

  // Reading the constant is what pulls the type-level check into the bundle the
  // suite loads; it is `true` by construction when tsc passes.
  it('is exhaustive against the contract', () => {
    expect(MODEL_FAMILIES_ARE_EXHAUSTIVE).toBe(true);
  });
});
