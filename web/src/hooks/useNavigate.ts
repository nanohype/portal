import { useLocation as useRouterLocation } from '@tanstack/react-router';
import { getRouter } from '@/lib/router-ref';

// Shims over TanStack Router so existing call sites keep their import surface.
// navigate() is a standalone helper (used outside hook scope), so it reaches the
// router via the ref holder rather than a hook.
export function navigate(to: string) {
  getRouter().navigate({ to });
}

// Returns "pathname + search" to match the prior contract (AppLayout splits on "?").
export function useLocation() {
  const loc = useRouterLocation();
  return loc.pathname + loc.searchStr;
}
