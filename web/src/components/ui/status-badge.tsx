import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { cn } from "@/lib/utils";
import type { StatusVisual } from "@/lib/status";

// The one status pill. Fed a StatusVisual from lib/status (one mapping per
// domain), so every status — run, cluster connection, ArgoCD, control plane,
// tenant phase/op — renders identically: a coloured badge with an icon (or a
// spinner while transitional) and a label. Renders nothing for a null visual
// (e.g. a control-plane / argo badge the watcher hasn't observed yet).
export function StatusBadge({
  visual,
  className,
}: {
  visual: StatusVisual | null | undefined;
  className?: string;
}) {
  if (!visual) return null;
  const Icon = visual.icon;
  return (
    <Badge
      variant={visual.variant}
      className={cn("gap-1.5", className)}
      aria-label={`Status: ${visual.label}`}
    >
      {visual.spinning ? (
        <Spinner className="w-3 h-3" aria-hidden="true" />
      ) : Icon ? (
        <Icon className="w-3 h-3" aria-hidden="true" />
      ) : null}
      {visual.label}
    </Badge>
  );
}
