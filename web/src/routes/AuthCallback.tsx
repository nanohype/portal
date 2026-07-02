import { useEffect } from 'react';
import { api } from '@/api/client';
import { useAuthStore } from '@/stores/auth';
import { navigate } from '@/hooks/useNavigate';
import { Spinner } from '@/components/ui/spinner';

// The server delivers the JWT via a short-lived HttpOnly cookie scoped to the
// handoff endpoint — never in a URL or in document.cookie, so it can't leak
// into history, proxy logs, Referer headers, or XSS-readable state. POSTing
// the handoff returns the token once; the server expires the cookie in the
// same response, so the exchange is single-use by construction.
//
// Single-flight: StrictMode double-invokes effects in dev, and a second POST
// would hit the already-expired cookie and 401. Both invocations share one
// in-flight request; the slot resets after it settles so a later login gets a
// fresh exchange.
let handoffInFlight: Promise<string | null> | null = null;

function exchangeHandoffToken(): Promise<string | null> {
  handoffInFlight ??= api
    .POST('/auth/handoff')
    .then(({ data }) => data?.token ?? null)
    .catch(() => null)
    .finally(() => {
      handoffInFlight = null;
    });
  return handoffInFlight;
}

export function AuthCallbackPage() {
  useEffect(() => {
    let cancelled = false;
    void exchangeHandoffToken().then((token) => {
      if (cancelled) return;
      if (token) {
        // Store the token, then navigate client-side — the protected route's
        // beforeLoad resolves /auth/me before rendering, so there's no reload flash.
        localStorage.setItem('portal_token', token);
        useAuthStore.setState({ token, isAuthenticated: true });
        navigate('/');
      } else {
        navigate('/login');
      }
    });
    return () => {
      cancelled = true;
    };
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
