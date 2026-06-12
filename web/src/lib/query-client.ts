import { QueryClient } from "@tanstack/react-query";

// Shared singleton so both the React tree (QueryClientProvider) and the router's
// beforeLoad auth gate (ensureQueryData) use the same cache.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 30_000,
      refetchOnWindowFocus: false,
    },
  },
});
