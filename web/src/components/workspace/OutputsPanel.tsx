import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { StateOutput } from '@/api/models';
import { Badge } from '@/components/ui/badge';
import { Spinner } from '@/components/ui/spinner';
import { Copy, ChevronDown, ChevronRight, Lock } from 'lucide-react';

interface Props {
  workspaceId: string;
}

function isComplexValue(value: unknown): boolean {
  return typeof value === 'object' && value !== null;
}

function formatValue(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'number') return String(value);
  return JSON.stringify(value, null, 2);
}

function formatInlineValue(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'number') return String(value);
  return JSON.stringify(value);
}

export function OutputsPanel({ workspaceId }: Props) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const {
    data: outputs,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ['outputs', workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/state/current/outputs', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data;
    },
  });

  const copyToClipboard = async (value: unknown) => {
    try {
      await navigator.clipboard.writeText(formatValue(value));
      toast.success('Copied to clipboard');
    } catch {
      toast.error('Failed to copy to clipboard');
    }
  };

  const toggleExpand = (name: string) => {
    setExpanded((prev) => ({ ...prev, [name]: !prev[name] }));
  };

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
        <p className="text-sm text-destructive">Failed to load outputs.</p>
      </div>
    );
  }

  const sorted = [...(outputs ?? [])].sort((a: StateOutput, b: StateOutput) =>
    a.name.localeCompare(b.name),
  );

  return (
    <div>
      <h3 className="text-base font-semibold mb-4">Outputs</h3>

      {!sorted.length ? (
        <div className="rounded-lg border border-dashed border-border p-8 text-center">
          <p className="text-sm text-muted-foreground">
            No outputs defined. Add output blocks to your OpenTofu configuration and run an apply.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border border-border divide-y divide-border">
          {sorted.map((output: StateOutput) => {
            const complex = !output.sensitive && isComplexValue(output.value);
            const isExpanded = expanded[output.name];

            return (
              <div key={output.name} className="px-4 py-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3 min-w-0">
                    {complex ? (
                      <button
                        onClick={() => toggleExpand(output.name)}
                        className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer shrink-0"
                      >
                        {isExpanded ? (
                          <ChevronDown className="w-3.5 h-3.5" />
                        ) : (
                          <ChevronRight className="w-3.5 h-3.5" />
                        )}
                      </button>
                    ) : (
                      <div className="w-4.5" />
                    )}
                    <code className="text-sm font-mono font-medium">{output.name}</code>
                    <Badge variant="outline" className="text-xs shrink-0">
                      {output.type}
                    </Badge>
                    {output.sensitive && (
                      <Lock className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                    )}
                  </div>
                  <div className="flex items-center gap-2 shrink-0 ml-4">
                    {output.sensitive ? (
                      <span className="text-sm font-mono text-muted-foreground">***</span>
                    ) : (
                      <span className="text-sm font-mono text-muted-foreground break-all">
                        {formatInlineValue(output.value)}
                      </span>
                    )}
                    {!output.sensitive && (
                      <button
                        onClick={() => copyToClipboard(output.value)}
                        className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                        title="Copy value"
                      >
                        <Copy className="w-3.5 h-3.5" />
                      </button>
                    )}
                  </div>
                </div>
                {complex && isExpanded && (
                  <pre className="mt-2 ml-7.5 p-3 rounded-lg bg-muted/50 border border-border text-xs font-mono overflow-auto max-h-60">
                    {formatValue(output.value)}
                  </pre>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
