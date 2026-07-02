import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { StateVersion, StateDiff } from '@/api/models';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Select } from '@/components/ui/select';
import { Spinner } from '@/components/ui/spinner';
import { useConfirm } from '@/components/ui/confirm-context';
import { ResourceBrowser } from './ResourceBrowser';
import { StateDiffViewer } from './StateDiffViewer';
import { formatRelativeTime } from '@/lib/utils';
import { useAuth } from '@/hooks/useAuth';
import { Database, Download, Layers, Search, GitCompare, Trash2 } from 'lucide-react';

interface Props {
  workspaceId: string;
}

async function downloadState(workspaceId: string, stateId: string) {
  const token = localStorage.getItem('portal_token');
  const res = await fetch(`/api/v1/workspaces/${workspaceId}/state/${stateId}/download`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    signal: AbortSignal.timeout(30_000),
  });
  if (!res.ok) {
    toast.error('Failed to download state file');
    return;
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `state-${stateId}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

export function StateExplorer({ workspaceId }: Props) {
  const [showResources, setShowResources] = useState(false);
  const [compareMode, setCompareMode] = useState(false);
  const [fromSerial, setFromSerial] = useState<number | ''>('');
  const [toSerial, setToSerial] = useState<number | ''>('');
  const { user } = useAuth();
  const canDrop = user?.role === 'owner' || user?.role === 'admin';
  const qc = useQueryClient();
  const confirm = useConfirm();

  const dropMutation = useMutation({
    mutationFn: async (serial: number) => {
      const token = localStorage.getItem('portal_token');
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/state/serial/${serial}`, {
        method: 'DELETE',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        signal: AbortSignal.timeout(30_000),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
    },
    onSuccess: () => {
      toast.success('State version dropped');
      qc.invalidateQueries({ queryKey: ['state-versions', workspaceId] });
      qc.invalidateQueries({ queryKey: ['state-current', workspaceId] });
    },
    onError: (e) => toast.error(`Failed to drop state version: ${(e as Error).message}`),
  });

  const {
    data: versions,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ['state-versions', workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/state', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const { data: currentState } = useQuery({
    queryKey: ['state-current', workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/state/current', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data;
    },
  });

  const canCompare =
    typeof fromSerial === 'number' && typeof toSerial === 'number' && fromSerial !== toSerial;

  const {
    data: diffResult,
    isLoading: isDiffLoading,
    isError: isDiffError,
  } = useQuery({
    queryKey: ['state-diff', workspaceId, fromSerial, toSerial],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/state/diff', {
        params: {
          path: { workspaceId },
          query: { from: fromSerial as number, to: toSerial as number },
        },
      });
      if (error) throw error;
      return data as StateDiff;
    },
    enabled: compareMode && canCompare,
  });

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Spinner className="w-5 h-5" />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-8 text-center">
        <p className="text-sm text-destructive">Failed to load state versions.</p>
      </div>
    );
  }

  return (
    <div>
      <h3 className="text-base font-semibold mb-4">State Versions</h3>

      {/* Current state summary */}
      {currentState && (
        <div className="mb-4 p-4 rounded-lg border border-primary/20 bg-primary/5">
          <div className="flex items-center gap-3 mb-2">
            <Database className="w-4 h-4 text-primary" />
            <span className="text-sm font-medium">Current State</span>
            <Badge variant="outline">Serial #{currentState.serial}</Badge>
          </div>
          <div className="flex items-center gap-4 text-sm text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <Layers className="w-3.5 h-3.5" />
              {currentState.resource_count} resources
            </span>
            <span className="font-mono text-xs">{currentState.resource_summary}</span>
            <span>{formatRelativeTime(currentState.created_at)}</span>
          </div>
          <div className="flex items-center gap-2 mt-3">
            <Button variant="outline" size="sm" onClick={() => setShowResources(!showResources)}>
              <Search className="w-3.5 h-3.5" />
              {showResources ? 'Hide Resources' : 'Browse Resources'}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setCompareMode(!compareMode);
                if (!compareMode && versions && versions.length >= 2) {
                  const sorted = [...(versions as StateVersion[])].sort(
                    (a, b) => b.serial - a.serial,
                  );
                  setFromSerial(sorted[1]?.serial ?? '');
                  setToSerial(sorted[0]?.serial ?? '');
                }
              }}
            >
              <GitCompare className="w-3.5 h-3.5" />
              {compareMode ? 'Hide Compare' : 'Compare'}
            </Button>
          </div>
        </div>
      )}

      {/* Resource Browser */}
      <ResourceBrowser workspaceId={workspaceId} enabled={showResources} />

      {/* Compare Mode */}
      {compareMode && versions && (versions as StateVersion[]).length >= 2 && (
        <div className="mt-4">
          <div className="flex items-center gap-3 mb-3">
            <div className="flex items-center gap-2">
              <label className="text-sm text-muted-foreground">From serial:</label>
              <Select
                value={String(fromSerial)}
                onChange={(e) => setFromSerial(Number(e.target.value))}
                placeholder="Select..."
              >
                <option value="">Select...</option>
                {(versions as StateVersion[]).map((sv) => (
                  <option key={sv.id} value={sv.serial}>
                    #{sv.serial}
                  </option>
                ))}
              </Select>
            </div>
            <span className="text-muted-foreground">→</span>
            <div className="flex items-center gap-2">
              <label className="text-sm text-muted-foreground">To serial:</label>
              <Select
                value={String(toSerial)}
                onChange={(e) => setToSerial(Number(e.target.value))}
                placeholder="Select..."
              >
                <option value="">Select...</option>
                {(versions as StateVersion[]).map((sv) => (
                  <option key={sv.id} value={sv.serial}>
                    #{sv.serial}
                  </option>
                ))}
              </Select>
            </div>
          </div>

          {isDiffLoading && (
            <div className="flex items-center justify-center py-6">
              <Spinner className="w-5 h-5" />
            </div>
          )}
          {isDiffError && (
            <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-4 text-center">
              <p className="text-sm text-destructive">Failed to load state diff.</p>
            </div>
          )}
          {diffResult && <StateDiffViewer diff={diffResult} />}
        </div>
      )}

      {!versions?.length ? (
        <div className="rounded-lg border border-dashed border-border p-8 text-center mt-4">
          <Database className="w-8 h-8 text-muted-foreground mx-auto mb-2" />
          <p className="text-sm text-muted-foreground">
            No state versions yet. Run an apply to create the first state.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border border-border divide-y divide-border mt-4">
          {(versions as StateVersion[]).map((sv) => (
            <div
              key={sv.id}
              className="flex items-center justify-between px-4 py-3 hover:bg-accent/50 transition-colors"
            >
              <div className="flex items-center gap-3">
                <span className="font-mono text-sm font-medium">#{sv.serial}</span>
                <span className="text-sm text-muted-foreground">{sv.resource_count} resources</span>
                <span className="font-mono text-xs text-muted-foreground">
                  {sv.resource_summary}
                </span>
              </div>
              <div className="flex items-center gap-3">
                <span className="text-xs text-muted-foreground">
                  {formatRelativeTime(sv.created_at)}
                </span>
                <button
                  onClick={() => downloadState(workspaceId, sv.id)}
                  className="p-1.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                  title="Download state file"
                >
                  <Download className="w-3.5 h-3.5" />
                </button>
                {canDrop && (
                  <button
                    onClick={async () => {
                      if (
                        await confirm({
                          title: `Drop state version #${sv.serial}?`,
                          message:
                            'This removes the DB row and S3 objects. Use only when the version is known-broken (e.g. partial-errored, undecryptable). Cannot be undone.',
                          confirmLabel: 'Drop',
                        })
                      ) {
                        dropMutation.mutate(sv.serial);
                      }
                    }}
                    disabled={dropMutation.isPending}
                    className="p-1.5 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors cursor-pointer disabled:opacity-50"
                    title="Drop this state version (admin)"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
