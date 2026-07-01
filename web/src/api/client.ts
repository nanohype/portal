import createClient from "openapi-fetch";
import type { paths } from "./types";

const API_BASE = "/api/v1";

// Default 30s deadline on every request; AbortSignal.any keeps
// caller-supplied signals working alongside it.
export const api = createClient<paths>({
  baseUrl: API_BASE,
  fetch: (request) =>
    fetch(request, {
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]),
    }),
});

// Add auth token to requests
api.use({
  onRequest({ request }) {
    const token = localStorage.getItem("portal_token");
    if (token) {
      request.headers.set("Authorization", `Bearer ${token}`);
    }
    return request;
  },
  onResponse({ response }) {
    if (response.status === 401) {
      localStorage.removeItem("portal_token");
      window.location.href = "/login";
    }
    return response;
  },
});
