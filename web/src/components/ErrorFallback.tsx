import { Button } from "@/components/ui/button";
import { navigate } from "@/hooks/useNavigate";
import { AlertTriangle } from "lucide-react";
import type { FallbackProps } from "react-error-boundary";

export function ErrorFallback({ error, resetErrorBoundary }: FallbackProps) {
  return (
    <div className="flex flex-col items-center justify-center min-h-[50vh] p-8 text-center">
      <AlertTriangle className="w-12 h-12 text-destructive mb-4" />
      <h2 className="text-xl font-semibold mb-2">Something went wrong</h2>
      <p className="text-sm text-muted-foreground mb-6 max-w-md">
        {error instanceof Error ? error.message : "An unexpected error occurred."}
      </p>
      <div className="flex items-center gap-3">
        <Button onClick={resetErrorBoundary}>Try again</Button>
        <Button variant="outline" onClick={() => navigate("/")}>
          Go to dashboard
        </Button>
      </div>
    </div>
  );
}
