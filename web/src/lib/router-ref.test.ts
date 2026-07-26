import { describe, expect, it } from 'vitest';
import type { AnyRouter } from '@tanstack/react-router';
import { getRouter, registerRouter } from './router-ref';

describe('router-ref', () => {
  it('throws rather than returning null before the router is registered', () => {
    // The holder exists to break an import cycle, so the failure mode it has to
    // avoid is a silent null reaching a caller that will dereference it. A
    // caller reading the router before bootstrap has a wiring bug, and the
    // message has to say so.
    expect(() => getRouter()).toThrow(/router accessed before registration/);
  });

  it('returns the registered router', () => {
    const router = { navigate: () => {} } as unknown as AnyRouter;
    registerRouter(router);
    expect(getRouter()).toBe(router);
  });

  it('re-registration replaces the previous router', () => {
    const first = { navigate: () => {} } as unknown as AnyRouter;
    const second = { navigate: () => {} } as unknown as AnyRouter;
    registerRouter(first);
    registerRouter(second);
    expect(getRouter()).toBe(second);
  });
});
