import { useState, useEffect } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { Workspace, CreateWorkspaceRequest } from '@/api/models';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { SkeletonCards } from '@/components/ui/skeleton';
import { RunStatusBadge } from '@/components/run/RunStatusBadge';
import { Pagination } from '@/components/ui/pagination';
import { CreateWorkspaceDialog } from './CreateWorkspaceDialog';
import { formatRelativeTime, getEnvironmentColor } from '@/lib/utils';
import type { RunStatus } from '@/api/models';
import { Link } from '@/components/ui/link';
import { useAuth } from '@/hooks/useAuth';
import { roleAtLeast } from '@/lib/roles';
import {
  Plus,
  GitBranch,
  FolderGit2,
  Upload,
  Clock,
  Lock,
  Zap,
  ShieldCheck,
  Webhook,
  Search,
  Layers,
} from 'lucide-react';

export function WorkspaceList() {
  const { user } = useAuth();
  // Creating a workspace stages real infrastructure, so the API gates it at
  // operator. Nothing exists to grant against yet, so this is the org role.
  const canCreate = roleAtLeast(user?.role, 'operator');
  const [showCreate, setShowCreate] = useState(false);
  const [search, setSearch] = useState('');
  const [debouncedSearch, setDebouncedSearch] = useState('');
  const [envFilter, setEnvFilter] = useState('');
  const [page, setPage] = useState(1);
  const queryClient = useQueryClient();

  // Debounce search input
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(search);
      setPage(1);
    }, 300);
    return () => clearTimeout(timer);
  }, [search]);

  const { data, isLoading, isError } = useQuery({
    queryKey: ['workspaces', page, debouncedSearch, envFilter],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces', {
        params: {
          query: {
            page,
            per_page: 20,
            search: debouncedSearch || undefined,
            environment: envFilter || undefined,
          },
        },
      });
      if (error) throw error;
      return data;
    },
  });

  const createMutation = useMutation({
    mutationFn: async (params: CreateWorkspaceRequest) => {
      const { data, error } = await api.POST('/workspaces', { body: params });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
      setShowCreate(false);
      toast.success('Workspace created');
    },
    onError: (e) =>
      toast.error((e as { message?: string })?.message ?? 'Failed to create workspace'),
  });

  const envOptions = [
    { label: 'All', value: '' },
    { label: 'Development', value: 'development' },
    { label: 'Staging', value: 'staging' },
    { label: 'Production', value: 'production' },
  ];

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Workspaces</h1>
          <p className="text-[12px] text-muted-foreground mt-1">Manage your OpenTofu workspaces</p>
        </div>
        {canCreate && (
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="w-4 h-4" />
            New workspace
          </Button>
        )}
      </div>

      {/* Search & filter bar */}
      <div className="flex items-center gap-3 mb-6">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
          <Input
            placeholder="Search workspaces..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
          />
        </div>
        <div className="flex items-center gap-1">
          {envOptions.map((opt) => (
            <Button
              key={opt.value}
              variant={envFilter === opt.value ? 'default' : 'outline'}
              size="sm"
              onClick={() => {
                setEnvFilter(opt.value);
                setPage(1);
              }}
            >
              {opt.label}
            </Button>
          ))}
        </div>
      </div>

      {isLoading ? (
        <SkeletonCards />
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-10 text-center">
            <p className="text-sm text-destructive">Failed to load workspaces. Please try again.</p>
          </div>
        </div>
      ) : !data?.data?.length ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-dashed border-border p-12 text-center">
            <FolderGit2 className="w-12 h-12 text-muted-foreground mx-auto mb-4" />
            <h3 className="text-lg font-medium mb-2">
              {debouncedSearch || envFilter ? 'No matching workspaces' : 'No workspaces yet'}
            </h3>
            <p className="text-muted-foreground mb-6 max-w-sm mx-auto">
              {debouncedSearch || envFilter
                ? 'Try adjusting your search or filter.'
                : canCreate
                  ? 'Create your first workspace to start managing OpenTofu infrastructure.'
                  : 'Nothing here yet. Creating a workspace takes an operator role or higher.'}
            </p>
            {/* Same bar as the header button: POST /workspaces is operator+.
                On a fresh install the first thing a viewer sees is this empty
                state, so an ungated button here offers them the one action the
                API is certain to refuse. */}
            {!debouncedSearch && !envFilter && canCreate && (
              <Button onClick={() => setShowCreate(true)}>
                <Plus className="w-4 h-4" />
                Create workspace
              </Button>
            )}
          </div>
        </div>
      ) : (
        <>
          <div className="grid gap-3" role="list" aria-label="Workspaces">
            {data.data.map((workspace: Workspace) => (
              <Link
                key={workspace.id}
                href={`/workspaces/${workspace.id}`}
                role="listitem"
                aria-label={`Workspace ${workspace.name}, ${workspace.environment}`}
                className="group block rounded-lg border border-border bg-card p-5 transition-all hover:border-border hover:shadow-lg hover:shadow-black/10"
              >
                <div className="flex items-start justify-between">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-3 mb-2">
                      <h3 className="font-semibold text-base group-hover:text-primary transition-colors">
                        {workspace.name}
                      </h3>
                      <Badge
                        className={getEnvironmentColor(workspace.environment)}
                        variant="outline"
                      >
                        {workspace.environment}
                      </Badge>
                      {workspace.last_run_status && (
                        <RunStatusBadge status={workspace.last_run_status as RunStatus} />
                      )}
                      {workspace.auto_apply && (
                        <Badge
                          variant="outline"
                          className="text-xs py-0 px-1.5 text-success border-success/30"
                        >
                          <Zap className="w-3 h-3 mr-0.5" />
                          auto
                        </Badge>
                      )}
                      {workspace.requires_approval && (
                        <Badge
                          variant="outline"
                          className="text-xs py-0 px-1.5 text-warning border-warning/30"
                        >
                          <ShieldCheck className="w-3 h-3 mr-0.5" />
                          approval
                        </Badge>
                      )}
                      {workspace.vcs_trigger_enabled && (
                        <Badge
                          variant="outline"
                          className="text-xs py-0 px-1.5 text-primary border-primary/30"
                        >
                          <Webhook className="w-3 h-3 mr-0.5" />
                          vcs
                        </Badge>
                      )}
                      {workspace.locked && (
                        <span aria-label="Locked">
                          <Lock className="w-3.5 h-3.5 text-warning" aria-hidden="true" />
                        </span>
                      )}
                    </div>
                    {workspace.description && (
                      <p className="text-sm text-muted-foreground mb-3">{workspace.description}</p>
                    )}
                    <div className="flex items-center gap-4 text-xs text-muted-foreground">
                      {workspace.source === 'upload' ? (
                        <span className="flex items-center gap-1.5">
                          <Upload className="w-3.5 h-3.5" />
                          Upload
                        </span>
                      ) : (
                        <span className="flex items-center gap-1.5">
                          <GitBranch className="w-3.5 h-3.5" />
                          {workspace.repo_branch}
                        </span>
                      )}
                      <span className="flex items-center gap-1.5">
                        <span className="font-mono">tofu {workspace.tofu_version}</span>
                      </span>
                      {(workspace.resource_count ?? 0) > 0 && (
                        <span className="flex items-center gap-1.5">
                          <Layers className="w-3.5 h-3.5" />
                          {workspace.resource_count} resources
                        </span>
                      )}
                      <span className="flex items-center gap-1.5">
                        <Clock className="w-3.5 h-3.5" />
                        {formatRelativeTime(workspace.updated_at)}
                      </span>
                    </div>
                  </div>
                </div>
              </Link>
            ))}
          </div>
          <Pagination page={page} perPage={20} total={data.total} onPageChange={setPage} />
        </>
      )}

      <CreateWorkspaceDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        onSubmit={(data) => createMutation.mutate(data)}
        isLoading={createMutation.isPending}
      />
    </div>
  );
}
