import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createApiClient } from './client';

// vitest's Request is undici's, which rejects a relative base.
const BASE = 'http://localhost/api/v1';

// Each case observes the Request the client hands to fetch, because the signal
// on that Request is the only place the deadline is visible. A stub that never
// settles keeps the request open so the abort is what ends it.
let captured: Request | undefined;

function stubHangingFetch() {
  captured = undefined;
  vi.stubGlobal('fetch', (input: Request) => {
    captured = input;
    return new Promise<Response>(() => {});
  });
}

function stubStatus(status: number) {
  vi.stubGlobal('fetch', (input: Request) => {
    captured = input;
    return Promise.resolve(
      new Response(status === 204 ? null : JSON.stringify({ error: 'nope' }), {
        status,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
}

const settle = (ms: number) => new Promise((r) => setTimeout(r, ms));

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('request deadline', () => {
  // A call that says nothing about timing still gets a bounded one.
  it('applies the default deadline when the call supplies no signal', async () => {
    stubHangingFetch();
    const api = createApiClient({ timeoutMs: 300, baseUrl: BASE });
    void api.GET('/health');
    await settle(20);
    expect(captured).toBeDefined();
    expect(captured?.signal.aborted).toBe(false);
    await settle(500);
    expect(captured?.signal.aborted).toBe(true);
  });

  // The regression this guards: composing the caller's signal with the default
  // via AbortSignal.any aborts on whichever fires first, so the default became a
  // ceiling and a longer caller deadline was unreachable through the client.
  // Under that behaviour this request is dead at 50ms.
  it('lets a caller-supplied signal replace the default rather than race it', async () => {
    stubHangingFetch();
    const api = createApiClient({ timeoutMs: 50, baseUrl: BASE });
    void api.GET('/health', { signal: AbortSignal.timeout(5_000) });
    await settle(20);
    expect(captured).toBeDefined();
    await settle(400);
    expect(captured?.signal.aborted).toBe(false);
  });

  it('still honours a caller signal that is shorter than the default', async () => {
    stubHangingFetch();
    const api = createApiClient({ timeoutMs: 5_000, baseUrl: BASE });
    void api.GET('/health', { signal: AbortSignal.timeout(50) });
    await settle(20);
    expect(captured).toBeDefined();
    await settle(300);
    expect(captured?.signal.aborted).toBe(true);
  });
});

describe('401 handling on the calls that used to bypass the client', () => {
  // These three ran as raw fetches with a hand-rolled Authorization header, so
  // they never reached onResponse: a token expiring mid-download or mid-upload
  // left the stored token in place and the session dead with no redirect.
  const calls: [string, (api: ReturnType<typeof createApiClient>) => Promise<unknown>][] = [
    [
      'state download',
      (api) =>
        api.GET('/workspaces/{workspaceId}/state/{stateId}/download', {
          params: { path: { workspaceId: 'w1', stateId: 's1' } },
          parseAs: 'blob',
        }),
    ],
    [
      'state version drop',
      (api) =>
        api.DELETE('/workspaces/{workspaceId}/state/serial/{serial}', {
          params: { path: { workspaceId: 'w1', serial: 3 } },
        }),
    ],
    [
      'config upload',
      (api) =>
        api.POST('/workspaces/{workspaceId}/upload', {
          params: { path: { workspaceId: 'w1' } },
          body: new FormData() as unknown as { file: string },
        }),
    ],
  ];

  it.each(calls)('clears the token and redirects on 401: %s', async (_name, call) => {
    stubStatus(401);
    const redirects: string[] = [];
    vi.stubGlobal('location', {
      get href() {
        return '';
      },
      set href(v: string) {
        redirects.push(v);
      },
    });
    localStorage.setItem('portal_token', 'expired-token');

    await call(createApiClient({ timeoutMs: 5_000, baseUrl: BASE }));

    expect(localStorage.getItem('portal_token')).toBeNull();
    expect(redirects).toEqual(['/login']);
  });

  it.each(calls)('sends the stored token on: %s', async (_name, call) => {
    stubStatus(204);
    localStorage.setItem('portal_token', 'live-token');
    await call(createApiClient({ timeoutMs: 5_000, baseUrl: BASE }));
    expect(captured?.headers.get('Authorization')).toBe('Bearer live-token');
  });
});
