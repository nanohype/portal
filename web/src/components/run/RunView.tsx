import { useEffect, useRef, useCallback, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { useRunStream } from '@/hooks/useRunStream';
import { RunStatusBadge } from './RunStatusBadge';
import { isRunInFlight } from '@/lib/status';
import { ApprovalPanel } from './ApprovalPanel';
import { PlanDiffViewer } from './PlanDiffViewer';
import { Button } from '@/components/ui/button';
import { Spinner } from '@/components/ui/spinner';
import { formatRelativeTime, formatDuration } from '@/lib/utils';
import { cn } from '@/lib/utils';
import { navigate } from '@/hooks/useNavigate';
import { Link } from '@/components/ui/link';
import { ArrowLeft, Clock, Timer, GitCommit, XCircle, RotateCcw } from 'lucide-react';
import type { RunStatus, TofuPlanJSON } from '@/api/models';

interface Props {
  workspaceId: string;
  runId: string;
}

type ViewTab = 'logs' | 'changes';

export function RunView({ workspaceId, runId }: Props) {
  const queryClient = useQueryClient();
  const termRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const [activeTab, setActiveTab] = useState<ViewTab>('logs');

  const { data: run, isLoading } = useQuery({
    queryKey: ['run', runId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/runs/{runId}', {
        params: { path: { workspaceId, runId } },
      });
      if (error) throw error;
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status && isRunInFlight(status) ? 3000 : false;
    },
  });

  const isRunning =
    run?.status === 'planning' || run?.status === 'applying' || run?.status === 'queued';

  const isTerminal =
    run?.status === 'planned' ||
    run?.status === 'awaiting_approval' ||
    run?.status === 'applied' ||
    run?.status === 'errored' ||
    run?.status === 'cancelled' ||
    run?.status === 'discarded';

  const hasChanges = !!run?.plan_json_url && isTerminal;

  const { data: planJSON } = useQuery({
    queryKey: ['plan-json', runId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/runs/{runId}/plan-json', {
        params: { path: { workspaceId, runId } },
      });
      if (error) throw error;
      return data as TofuPlanJSON;
    },
    enabled: activeTab === 'changes' && !!run?.plan_json_url,
  });

  const isCancellable =
    run?.status === 'pending' ||
    run?.status === 'queued' ||
    run?.status === 'planning' ||
    run?.status === 'applying' ||
    run?.status === 'awaiting_approval';

  const cancelMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/runs/{runId}/cancel', {
        params: { path: { workspaceId, runId } },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['run', runId] });
      toast.success('Run cancelled');
    },
    onError: () => toast.error('Failed to cancel run'),
  });

  const isRetryable = run?.status === 'errored' || run?.status === 'cancelled';

  const retryMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/runs', {
        params: { path: { workspaceId } },
        body: { operation: run?.operation },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: (newRun) => {
      toast.success('Retry run created');
      navigate(`/workspaces/${workspaceId}/runs/${newRun.id}`);
    },
    // Retrying repeats the operation, and an apply on a workspace that requires
    // approval is refused with a reason — a plan and a signature. Say it.
    onError: (e) => toast.error((e as { message?: string })?.message ?? 'Failed to retry run'),
  });

  const handleData = useCallback((data: string) => {
    terminalRef.current?.write(data);
  }, []);

  // Connect WebSocket for running states (real-time) and for finished states
  // without plan_output (to replay buffered logs, e.g. errored runs).
  const needsLogReplay = isTerminal && !run?.plan_output;

  useRunStream({
    runId,
    workspaceId,
    enabled: isRunning || needsLogReplay,
    onData: handleData,
  });

  // Track whether we've written stored output to avoid duplicating
  const wroteOutputRef = useRef(false);
  const [termReady, setTermReady] = useState(false);

  // Initialize terminal once on mount
  useEffect(() => {
    if (!termRef.current || terminalRef.current) return;

    const terminal = new Terminal({
      theme: {
        background: '#0a0a0a',
        foreground: '#e5e5e5',
        cursor: '#e5e5e5',
        selectionBackground: '#3b82f620',
        black: '#0a0a0a',
        red: '#ef4444',
        green: '#22c55e',
        yellow: '#eab308',
        blue: '#3b82f6',
        magenta: '#a855f7',
        cyan: '#06b6d4',
        white: '#e5e5e5',
      },
      fontSize: 13,
      fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", monospace',
      cursorBlink: false,
      disableStdin: true,
      scrollback: 10000,
      convertEol: true,
    });

    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(termRef.current);
    fitAddon.fit();

    terminalRef.current = terminal;
    fitAddonRef.current = fitAddon;
    wroteOutputRef.current = false;
    setTermReady(true);

    const observer = new ResizeObserver(() => {
      fitAddon.fit();
    });
    observer.observe(termRef.current);

    return () => {
      observer.disconnect();
      terminal.dispose();
      terminalRef.current = null;
    };
  }, []);

  // Write plan_output to terminal when terminal is ready and run data is available
  useEffect(() => {
    if (!termReady || !terminalRef.current || wroteOutputRef.current) return;
    if (run?.plan_output && isTerminal) {
      terminalRef.current.clear();
      terminalRef.current.write(run.plan_output.replace(/\n/g, '\r\n'));
      wroteOutputRef.current = true;
    }
  }, [termReady, run?.plan_output, isTerminal]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <Spinner className="w-6 h-6" />
      </div>
    );
  }

  if (!run) {
    return (
      <div className="p-8 text-center">
        <p className="text-muted-foreground">Run not found</p>
      </div>
    );
  }

  const showApproval =
    run.status === 'planned' ||
    run.status === 'awaiting_approval' ||
    run.status === 'applied' ||
    run.status === 'discarded';

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <div className="p-6 border-b border-border">
        <Link
          href={`/workspaces/${workspaceId}`}
          className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors mb-3"
        >
          <ArrowLeft className="w-3.5 h-3.5" />
          Back to workspace
        </Link>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-4">
            <h1 className="text-lg font-semibold tracking-tight">
              {run.operation.charAt(0).toUpperCase() + run.operation.slice(1)}{' '}
              <span className="font-mono text-base text-muted-foreground">
                {run.id.slice(0, 12)}
              </span>
            </h1>
            <RunStatusBadge status={run.status as RunStatus} />
          </div>

          <div className="flex items-center gap-4 text-sm text-muted-foreground">
            {isCancellable && (
              <Button
                variant="outline"
                size="sm"
                aria-label="Cancel run"
                className="text-destructive hover:bg-destructive/10"
                onClick={() => cancelMutation.mutate()}
                disabled={cancelMutation.isPending}
              >
                <XCircle className="w-3.5 h-3.5" />
                Cancel
              </Button>
            )}
            {isRetryable && (
              <Button
                variant="outline"
                size="sm"
                aria-label="Retry run"
                onClick={() => retryMutation.mutate()}
                disabled={retryMutation.isPending}
              >
                <RotateCcw className="w-3.5 h-3.5" />
                Retry
              </Button>
            )}
            <span className="flex items-center gap-1.5">
              <Clock className="w-4 h-4" />
              {formatRelativeTime(run.created_at)}
            </span>
            {run.started_at && (
              <span className="flex items-center gap-1.5">
                <Timer className="w-4 h-4" />
                {formatDuration(run.started_at, run.finished_at)}
              </span>
            )}
            {run.commit_sha && (
              <span className="flex items-center gap-1.5 font-mono text-xs">
                <GitCommit className="w-3.5 h-3.5" />
                {run.commit_sha.slice(0, 8)}
              </span>
            )}
          </div>
        </div>

        {/* Resource change summary */}
        {run.resources_added || run.resources_changed || run.resources_deleted ? (
          <div className="flex items-center gap-4 mt-3 text-sm font-mono">
            {run.resources_added ? (
              <span className="text-success">+{run.resources_added} to add</span>
            ) : null}
            {run.resources_changed ? (
              <span className="text-warning">~{run.resources_changed} to change</span>
            ) : null}
            {run.resources_deleted ? (
              <span className="text-destructive">-{run.resources_deleted} to destroy</span>
            ) : null}
          </div>
        ) : null}

        {run.error_message && (
          <div className="mt-3 p-3 rounded-lg bg-destructive/10 border border-destructive/20 text-sm text-destructive">
            {run.error_message}
          </div>
        )}
      </div>

      {/* Tab bar — only show when Changes tab is available */}
      {hasChanges && (
        <div className="flex border-b border-border bg-card" role="tablist" aria-label="Run output">
          {(['logs', 'changes'] as const).map((tab) => (
            <button
              key={tab}
              role="tab"
              aria-selected={activeTab === tab}
              onClick={() => setActiveTab(tab)}
              className={cn(
                'px-4 py-2 text-sm font-medium transition-colors cursor-pointer',
                activeTab === tab
                  ? 'text-foreground border-b-2 border-primary'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {tab === 'logs' ? 'Logs' : 'Changes'}
            </button>
          ))}
        </div>
      )}

      {/* Logs tab */}
      {activeTab === 'logs' && (
        <div className="flex-1 min-h-0 overflow-auto bg-[#0a0a0a]">
          {/* Live terminal for running jobs */}
          <div
            ref={termRef}
            className={cn('h-full', isTerminal && run?.plan_output && 'hidden')}
            role="log"
            aria-label="Run output logs"
          />
          {/* Static log output for finished runs */}
          {isTerminal && run?.plan_output && (
            <pre className="p-4 text-sm font-mono text-[#e5e5e5] whitespace-pre-wrap">
              {run.plan_output}
            </pre>
          )}
        </div>
      )}

      {/* Changes tab */}
      {activeTab === 'changes' && hasChanges && run.plan_output && (
        <div className="flex-1 min-h-0 overflow-auto">
          <PlanDiffViewer planOutput={run.plan_output} planJSON={planJSON} />
        </div>
      )}

      {/* Approval panel */}
      {showApproval && (
        <ApprovalPanel
          workspaceId={workspaceId}
          runId={runId}
          runStatus={run.status as RunStatus}
        />
      )}
    </div>
  );
}
