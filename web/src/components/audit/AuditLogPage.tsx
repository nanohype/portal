import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { AuditLog } from '@/api/models';
import { Spinner } from '@/components/ui/spinner';
import { Badge } from '@/components/ui/badge';
import { formatRelativeTime } from '@/lib/utils';
import { cn } from '@/lib/utils';
import { Shield, ChevronDown, ChevronRight } from 'lucide-react';

export function AuditLogPage() {
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [page, setPage] = useState(1);

  const {
    data: logs,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ['audit-logs', page],
    queryFn: async () => {
      const { data, error } = await api.GET('/audit-logs', {
        params: { query: { page, per_page: 50 } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="mb-6">
        <h1 className="text-lg font-semibold tracking-tight">Audit Logs</h1>
        <p className="text-[12px] text-muted-foreground mt-1">
          Activity history across the organization.
        </p>
      </div>

      {isLoading ? (
        <div className="flex-1 flex items-center justify-center">
          <Spinner className="w-6 h-6" />
        </div>
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-10 text-center">
            <p className="text-sm text-destructive">Failed to load audit logs. Please try again.</p>
          </div>
        </div>
      ) : !logs?.length ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-dashed border-border p-10 text-center">
            <Shield className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
            <h3 className="font-medium mb-1">No audit logs yet</h3>
            <p className="text-sm text-muted-foreground">
              Activity will appear here as actions are performed.
            </p>
          </div>
        </div>
      ) : (
        <>
          <div className="rounded-lg border border-border divide-y divide-border">
            {(logs as AuditLog[]).map((log) => (
              <div key={log.id}>
                <button
                  onClick={() => setExpandedId(expandedId === log.id ? null : log.id)}
                  className="w-full flex items-center gap-3 px-4 py-3 hover:bg-accent/50 transition-colors text-left cursor-pointer"
                >
                  {expandedId === log.id ? (
                    <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" />
                  ) : (
                    <ChevronRight className="w-4 h-4 text-muted-foreground shrink-0" />
                  )}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium font-mono">{log.action}</span>
                      <Badge variant="outline" className="text-xs">
                        {log.entity_type}
                      </Badge>
                      <span className="text-xs text-muted-foreground font-mono">
                        {log.entity_id ? log.entity_id.slice(0, 12) : '—'}
                      </span>
                    </div>
                    <div className="text-xs text-muted-foreground mt-0.5">
                      by {log.user_id ? log.user_id.slice(0, 12) : 'system'} ·{' '}
                      {formatRelativeTime(log.created_at)}
                      {log.ip_address && (
                        <>
                          {' · '}
                          {log.ip_address}
                        </>
                      )}
                    </div>
                  </div>
                </button>

                {expandedId === log.id && (
                  <div className="px-4 pb-4 pt-1 border-t border-border/50">
                    <div className="grid grid-cols-2 gap-4">
                      <div>
                        <div className="text-xs font-medium text-muted-foreground mb-1">Before</div>
                        <pre
                          className={cn(
                            'text-xs font-mono p-3 rounded-lg overflow-auto max-h-64',
                            'bg-accent/30 text-foreground/80',
                          )}
                        >
                          {log.before_data ? JSON.stringify(log.before_data, null, 2) : 'null'}
                        </pre>
                      </div>
                      <div>
                        <div className="text-xs font-medium text-muted-foreground mb-1">After</div>
                        <pre
                          className={cn(
                            'text-xs font-mono p-3 rounded-lg overflow-auto max-h-64',
                            'bg-accent/30 text-foreground/80',
                          )}
                        >
                          {log.after_data ? JSON.stringify(log.after_data, null, 2) : 'null'}
                        </pre>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            ))}
          </div>

          {/* Pagination */}
          <div className="flex items-center justify-between mt-4">
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1}
              className="text-sm text-muted-foreground hover:text-foreground disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
            >
              Previous
            </button>
            <span className="text-sm text-muted-foreground">Page {page}</span>
            <button
              onClick={() => setPage((p) => p + 1)}
              disabled={(logs?.length ?? 0) < 50}
              className="text-sm text-muted-foreground hover:text-foreground disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
            >
              Next
            </button>
          </div>
        </>
      )}
    </div>
  );
}
