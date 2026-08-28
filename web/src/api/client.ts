import createClient from 'openapi-fetch';
import type { paths } from './types';

const API_BASE = '/api/v1';

/** Deadline applied to a request that supplies no signal of its own. */
export const DEFAULT_TIMEOUT_MS = 30_000;

const REQUEST_METHODS = new Set([
  'GET',
  'PUT',
  'POST',
  'DELETE',
  'OPTIONS',
  'HEAD',
  'PATCH',
  'TRACE',
]);

// The deadline is attached per call, not inside the client's `fetch` hook,
// because a caller's signal has to REPLACE it rather than race it. Composing
// the two with AbortSignal.any aborts on whichever fires first, which turns the
// default into a ceiling: an upload asking for 120s would still die at 30, and
// the only way to get a longer deadline is to leave the client — which is what
// the config upload used to do, losing the 401 handling below with it.
//
// The per-call boundary is also the only place the distinction is visible. The
// `fetch` hook receives a Request, and a Request always carries a signal
// whether the caller supplied one or not, so by then the two cases cannot be
// told apart.
// `baseUrl` is a parameter because a relative base only resolves against a
// document origin; outside a browser, Request rejects it.
export function createApiClient({
  timeoutMs = DEFAULT_TIMEOUT_MS,
  baseUrl = API_BASE,
}: {
  timeoutMs?: number;
  baseUrl?: string;
} = {}) {
  const client = createClient<paths>({ baseUrl });

  client.use({
    onRequest({ request }) {
      const token = localStorage.getItem('portal_token');
      if (token) {
        request.headers.set('Authorization', `Bearer ${token}`);
      }
      return request;
    },
    onResponse({ response }) {
      if (response.status === 401) {
        localStorage.removeItem('portal_token');
        window.location.href = '/login';
      }
      return response;
    },
  });

  return new Proxy(client, {
    get(target, prop, receiver) {
      const value = Reflect.get(target, prop, receiver);
      if (typeof value !== 'function') {
        return value;
      }
      if (typeof prop !== 'string' || !REQUEST_METHODS.has(prop)) {
        return value.bind(target);
      }
      return (path: unknown, init?: Record<string, unknown>) =>
        (value as (p: unknown, i?: unknown) => unknown).call(target, path, {
          ...init,
          signal: init?.signal ?? AbortSignal.timeout(timeoutMs),
        });
    },
  });
}

export const api = createApiClient();
