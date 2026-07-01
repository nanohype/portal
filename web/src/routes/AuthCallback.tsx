import { useEffect } from "react";
import { useAuthStore } from "@/stores/auth";
import { navigate } from "@/hooks/useNavigate";
import { Spinner } from "@/components/ui/spinner";

const HANDOFF_COOKIE = "auth_token";

// The server delivers the JWT via a short-lived cookie scoped to this route
// (never in a URL, so it can't leak into history, proxy logs, or Referer
// headers). Read it once, then delete it.
function takeHandoffToken(): string | null {
  const entry = document.cookie
    .split("; ")
    .find((c) => c.startsWith(`${HANDOFF_COOKIE}=`));
  if (!entry) return null;
  document.cookie = `${HANDOFF_COOKIE}=; Path=/auth/callback; Max-Age=0`;
  return entry.slice(HANDOFF_COOKIE.length + 1) || null;
}

export function AuthCallbackPage() {
  useEffect(() => {
    const token = takeHandoffToken();

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
