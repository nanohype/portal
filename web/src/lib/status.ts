import type { BadgeProps } from '@/components/ui/badge';
import type {
  ClusterConnectionStatus,
  PipelineRunStatus,
  PipelineStageStatus,
  RunStatus,
} from '@/api/models';
import {
  Ban,
  CheckCircle2,
  CircleDot,
  Clock,
  ShieldQuestion,
  SkipForward,
  XCircle,
  type LucideIcon,
} from 'lucide-react';

// One status vocabulary for the whole app. Each domain maps its status → a
// StatusVisual here (one place), and <StatusBadge> renders it (one place) — so
// the eight ad-hoc switches that used to reimplement this are gone and status
// reads the same everywhere. A null return means "don't render a badge".
export interface StatusVisual {
  label: string;
  variant: BadgeProps['variant'];
  icon?: LucideIcon;
  spinning?: boolean;
}

// Terraform/OpenTofu run lifecycle.
export const runStatus: Record<RunStatus, StatusVisual> = {
  pending: { label: 'Pending', variant: 'secondary', icon: Clock },
  queued: { label: 'Queued', variant: 'secondary', icon: Clock },
  planning: { label: 'Planning', variant: 'warning', icon: CircleDot, spinning: true },
  planned: { label: 'Planned', variant: 'default', icon: CheckCircle2 },
  awaiting_approval: { label: 'Needs Approval', variant: 'warning', icon: ShieldQuestion },
  applying: { label: 'Applying', variant: 'warning', icon: CircleDot, spinning: true },
  applied: { label: 'Applied', variant: 'success', icon: CheckCircle2 },
  errored: { label: 'Errored', variant: 'destructive', icon: XCircle },
  cancelled: { label: 'Cancelled', variant: 'secondary', icon: Ban },
  discarded: { label: 'Discarded', variant: 'secondary', icon: Ban },
};

// Run statuses that are still moving — or may move without user action (a
// `planned` run can auto-apply or sit awaiting approval). Used to decide whether
// a view should keep polling. Terminal: applied, errored, cancelled, discarded.
const RUN_IN_FLIGHT: ReadonlySet<RunStatus> = new Set([
  'pending',
  'queued',
  'planning',
  'planned',
  'awaiting_approval',
  'applying',
]);
export function isRunInFlight(status: RunStatus): boolean {
  return RUN_IN_FLIGHT.has(status);
}

// A pipeline run is still moving until it reaches a terminal — `idle` is the
// created-but-not-yet-picked-up state, `running` is mid-flight. Terminal:
// completed, errored, cancelled.
const PIPELINE_RUN_IN_FLIGHT: ReadonlySet<PipelineRunStatus> = new Set(['idle', 'running']);
export function isPipelineRunInFlight(status: PipelineRunStatus): boolean {
  return PIPELINE_RUN_IN_FLIGHT.has(status);
}

// Pipeline run lifecycle (the whole run).
const PIPELINE_RUN: Record<PipelineRunStatus, StatusVisual> = {
  idle: { label: 'Idle', variant: 'secondary', icon: Clock },
  running: { label: 'Running', variant: 'default', icon: CircleDot, spinning: true },
  completed: { label: 'Completed', variant: 'success', icon: CheckCircle2 },
  errored: { label: 'Errored', variant: 'destructive', icon: XCircle },
  cancelled: { label: 'Cancelled', variant: 'secondary', icon: Ban },
};
export function pipelineRunStatus(status: PipelineRunStatus): StatusVisual {
  return PIPELINE_RUN[status] ?? PIPELINE_RUN.idle;
}

// A single pipeline stage. importing_outputs rides as a spinner like running —
// it's the brief hand-off between stages.
const PIPELINE_STAGE: Record<PipelineStageStatus, StatusVisual> = {
  pending: { label: 'Pending', variant: 'secondary', icon: Clock },
  importing_outputs: { label: 'Importing', variant: 'default', icon: CircleDot, spinning: true },
  running: { label: 'Running', variant: 'default', icon: CircleDot, spinning: true },
  awaiting_approval: { label: 'Awaiting Approval', variant: 'warning', icon: ShieldQuestion },
  completed: { label: 'Completed', variant: 'success', icon: CheckCircle2 },
  errored: { label: 'Errored', variant: 'destructive', icon: XCircle },
  skipped: { label: 'Skipped', variant: 'secondary', icon: SkipForward },
  cancelled: { label: 'Cancelled', variant: 'secondary', icon: Ban },
};
export function pipelineStageStatus(status: PipelineStageStatus): StatusVisual {
  return PIPELINE_STAGE[status] ?? PIPELINE_STAGE.pending;
}

// Portal's async connection test to a registered cluster.
const CLUSTER_CONNECTION: Record<ClusterConnectionStatus, StatusVisual> = {
  pending: { label: 'Pending', variant: 'secondary', icon: Clock },
  connecting: { label: 'Connecting', variant: 'default', icon: CircleDot, spinning: true },
  connected: { label: 'Connected', variant: 'success', icon: CheckCircle2 },
  failed: { label: 'Failed', variant: 'destructive', icon: XCircle },
};
export function clusterConnection(status: ClusterConnectionStatus): StatusVisual {
  return CLUSTER_CONNECTION[status] ?? CLUSTER_CONNECTION.pending;
}

// EKS control-plane lifecycle (eks:DescribeCluster). Null when not observed
// (non-EKS, or describe not permitted yet).
export function controlPlane(status: string): StatusVisual | null {
  if (!status) return null;
  const label = status.charAt(0) + status.slice(1).toLowerCase();
  switch (status) {
    case 'ACTIVE':
      return { label, variant: 'success', icon: CheckCircle2 };
    case 'UPDATING':
    case 'CREATING':
      return { label, variant: 'warning', icon: CircleDot, spinning: true };
    case 'DEGRADED':
    case 'FAILED':
      return { label, variant: 'destructive', icon: XCircle };
    default:
      return { label, variant: 'secondary', icon: Clock };
  }
}

// ArgoCD sync + health folded into one badge — colour follows health
// (Progressing is the transitional/warning state). Null when the watcher hasn't
// observed a per-cluster Application (hand-registered clusters, or pre-first-tick).
export function argo(sync: string, health: string): StatusVisual | null {
  if (!sync && !health) return null;
  const variant: BadgeProps['variant'] =
    health === 'Healthy'
      ? 'success'
      : health === 'Progressing'
        ? 'warning'
        : health === 'Degraded' || health === 'Missing'
          ? 'destructive'
          : sync === 'OutOfSync'
            ? 'warning'
            : 'secondary';
  const icon =
    variant === 'success' ? CheckCircle2 : variant === 'destructive' ? XCircle : CircleDot;
  return {
    label: [sync, health].filter(Boolean).join(' · '),
    variant,
    icon,
    spinning: health === 'Progressing',
  };
}

// eks-agent-platform tenant phase (free-form string from the operator).
export function tenantPhase(phase: string): StatusVisual {
  switch (phase.toLowerCase()) {
    case 'ready':
    case 'active':
    case 'healthy':
      return { label: phase, variant: 'success', icon: CheckCircle2 };
    case 'pending':
    case 'provisioning':
      return { label: phase, variant: 'default', icon: CircleDot, spinning: true };
    case 'error':
    case 'failed':
    case 'degraded':
      return { label: phase, variant: 'destructive', icon: XCircle };
    case '':
      return { label: 'Unknown', variant: 'secondary', icon: Clock };
    default:
      return { label: phase, variant: 'secondary', icon: CircleDot };
  }
}

// A tenant phase still moving — used to decide whether to poll fast. Anything
// else (ready/healthy/degraded/failed/...) is at rest from the watcher's view.
export function isTenantPhaseTransitional(phase: string): boolean {
  const p = phase.toLowerCase();
  return p === 'pending' || p === 'provisioning';
}

// Tenant op status. Tenant ops terminate at commit (no watcher advances them),
// so they ride as a status, not a phase timeline. Factual: "committed" means
// applied to git, not yet proven reconciled.
export function tenantOpStatus(status: string): StatusVisual {
  switch (status) {
    case 'committed':
      return { label: 'Committed', variant: 'default', icon: CheckCircle2 };
    case 'failed':
      return { label: 'Failed', variant: 'destructive', icon: XCircle };
    default:
      return { label: 'Pending', variant: 'secondary', icon: Clock };
  }
}
