import { useEffect } from "react";
import { useAuthStore } from "@/stores/auth";
import { Spinner } from "@/components/ui/spinner";

export function AuthCallbackPage() {
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const token = params.get("token");

    if (token) {
      // Store token — user will be fetched by useAuth hook
      localStorage.setItem("tofui_token", token);
      useAuthStore.setState({ token, isAuthenticated: true });
      window.location.href = "/";
    } else {
      window.location.href = "/login";
    }
  }, []);

  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="text-center">
        <Spinner className="w-8 h-8 mx-auto mb-4" />
        <p className="text-muted-foreground">Signing you in...</p>
      </div>
    </div>
  );
}
