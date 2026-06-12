import { useEffect } from "react";
import { useAuthStore } from "@/stores/auth";
import { navigate } from "@/hooks/useNavigate";
import { Spinner } from "@/components/ui/spinner";

export function AuthCallbackPage() {
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const token = params.get("token");

    if (token) {
      // Store the token, then navigate client-side — the protected route's
      // beforeLoad resolves /auth/me before rendering, so there's no reload flash.
      localStorage.setItem("portal_token", token);
      useAuthStore.setState({ token, isAuthenticated: true });
      navigate("/");
    } else {
      navigate("/login");
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
