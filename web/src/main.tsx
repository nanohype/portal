import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { queryClient } from "@/lib/query-client";
import { router } from "@/router";
import { useTheme } from "@/stores/theme";
import { ConfirmProvider } from "@/components/ui/confirm";
import "./index.css";

// Toasts follow the app theme so richColors render on the right surface.
// eslint-disable-next-line react-refresh/only-export-components -- bootstrap entry (createRoot); local toaster, not an HMR boundary
function ThemedToaster() {
  const theme = useTheme((s) => s.theme);
  return <Toaster position="bottom-right" richColors closeButton theme={theme} />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ConfirmProvider>
        <RouterProvider router={router} />
      </ConfirmProvider>
      <ThemedToaster />
    </QueryClientProvider>
  </StrictMode>
);
