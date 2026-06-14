import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { queryClient } from "@/lib/query-client";
import { router } from "@/router";
import { useTheme } from "@/stores/theme";
import "./index.css";

// Toasts follow the app theme so richColors render on the right surface.
function ThemedToaster() {
  const theme = useTheme((s) => s.theme);
  return <Toaster position="bottom-right" richColors closeButton theme={theme} />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <ThemedToaster />
    </QueryClientProvider>
  </StrictMode>
);
