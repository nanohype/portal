import type { RunStatus } from "@/api/types";
import { StatusBadge } from "@/components/ui/status-badge";
import { runStatus } from "@/lib/status";

export function RunStatusBadge({ status }: { status: RunStatus }) {
  return <StatusBadge visual={runStatus[status] ?? runStatus.pending} />;
}
