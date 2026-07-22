import { useId, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { PipelineRun, PipelineVariable } from '@/api/models';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { Spinner } from '@/components/ui/spinner';
import { Link } from '@/components/ui/link';
import { Pagination } from '@/components/ui/pagination';
import { useConfirm } from '@/components/ui/confirm-context';
import { navigate } from '@/hooks/useNavigate';
import { StatusBadge } from '@/components/ui/status-badge';
import { formatDuration } from '@/lib/utils';
import { isPipelineRunInFlight, pipelineRunStatus } from '@/lib/status';
import { roleAtLeast } from '@/lib/roles';
import { useAuth } from '@/hooks/useAuth';
import { GitBranch, Play, ArrowLeft, ChevronRight, Plus, Pencil, Trash2, Lock } from 'lucide-react';

export function PipelineDetail({ pipelineId }: { pipelineId: string }) {
  const [tab, setTab] = useState<'stages' | 'variables' | 'runs'>('stages');
  const [page, setPage] = useState(1);
  const queryClient = useQueryClient();

  const { data, isLoading, isError } = useQuery({
    queryKey: ['pipeline', pipelineId],
    queryFn: async () => {
      const { data, error } = await api.GET('/pipelines/{pipelineId}', {
        params: { path: { pipelineId } },
      });
      if (error) throw error;
      return data!;
    },
  });

  const { data: runsData } = useQuery({
    queryKey: ['pipeline-runs', pipelineId, page],
    queryFn: async () => {
      const { data, error } = await api.GET('/pipelines/{pipelineId}/runs', {
        params: { path: { pipelineId }, query: { page, per_page: 20 } },
      });
      if (error) throw error;
      return data!;
    },
    enabled: tab === 'runs',
    // Keep the runs list live while any pipeline run is still advancing, so a
    // run started here progresses through its stages without a manual refresh.
    refetchInterval: (query) =>
      query.state.data?.data?.some((r) => isPipelineRunInFlight(r.status)) ? 3000 : false,
  });

  const startRunMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/pipelines/{pipelineId}/runs', {
        params: { path: { pipelineId } },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: (pr) => {
      queryClient.invalidateQueries({
        queryKey: ['pipeline-runs', pipelineId],
      });
      toast.success('Pipeline run started');
      navigate(`/pipelines/${pipelineId}/runs/${pr.id}`);
    },
    onError: () => toast.error('Failed to start pipeline run'),
  });

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load pipeline.
        </div>
      </div>
    );
  }

  const { pipeline, stages } = data;

  return (
    <div className="p-6 animate-fade-up">
      {/* Header */}
      <div className="mb-6">
        <Link
          href="/pipelines"
          className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 mb-3 transition-colors"
        >
          <ArrowLeft className="w-3 h-3" />
          Pipelines
        </Link>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-9 h-9 rounded-lg bg-primary/10 flex items-center justify-center">
              <GitBranch className="w-4 h-4 text-primary" />
            </div>
            <div>
              <h1 className="text-lg font-semibold tracking-tight">{pipeline.name}</h1>
              {pipeline.description && (
                <p className="text-xs text-muted-foreground mt-0.5">{pipeline.description}</p>
              )}
            </div>
          </div>
          <Button
            size="sm"
            onClick={() => startRunMutation.mutate()}
            disabled={startRunMutation.isPending}
          >
            <Play className="w-3.5 h-3.5" />
            {startRunMutation.isPending ? 'Starting...' : 'Run Pipeline'}
          </Button>
        </div>
      </div>

      {/* Tabs */}
      <div className="flex gap-0.5 border-b border-border/50 mb-6" role="tablist">
        {(['stages', 'variables', 'runs'] as const).map((t) => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`px-4 py-2 text-xs font-medium border-b-2 transition-all duration-150 cursor-pointer ${
              tab === t
                ? 'border-primary text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            {t === 'stages'
              ? `Stages (${stages.length})`
              : t === 'variables'
                ? 'Variables'
                : 'Runs'}
          </button>
        ))}
      </div>

      {/* Stages tab */}
      {tab === 'stages' && (
        <div className="relative">
          {/* Vertical connector line */}
          {stages.length > 1 && (
            <div
              className="absolute left-[19px] top-[44px] w-px bg-border/50"
              style={{ height: `calc(100% - 64px)` }}
            />
          )}
          <div className="space-y-2">
            {stages.map((stage, i) => (
              <div
                key={stage.id}
                className="relative flex items-center gap-3 animate-fade-up"
                style={{ animationDelay: `${i * 40}ms` }}
              >
                {/* Step indicator */}
                <div className="w-10 h-10 rounded-full border-2 border-border/60 bg-card flex items-center justify-center z-10 shrink-0">
                  <span className="text-[11px] font-semibold text-muted-foreground">
                    {stage.stage_order + 1}
                  </span>
                </div>
                {/* Card */}
                <div className="flex-1 border border-border/50 rounded-lg px-4 py-3 hover:border-primary/20 hover:bg-accent/20 transition-all duration-150">
                  <div className="flex items-center justify-between">
                    <Link
                      href={`/workspaces/${stage.workspace_id}`}
                      className="text-sm font-medium hover:text-primary transition-colors"
                    >
                      {stage.workspace_name}
                    </Link>
                    <div className="flex items-center gap-1.5">
                      <Badge variant={stage.auto_apply ? 'success' : 'secondary'}>
                        {stage.auto_apply ? 'auto-apply' : 'manual'}
                      </Badge>
                      <Badge variant={stage.on_failure === 'stop' ? 'destructive' : 'warning'}>
                        on fail: {stage.on_failure}
                      </Badge>
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Variables tab */}
      {tab === 'variables' && <PipelineVariablesTab pipelineId={pipelineId} />}

      {/* Runs tab */}
      {tab === 'runs' && (
        <div>
          {runsData?.data && runsData.data.length === 0 ? (
            <div className="text-center py-16 text-muted-foreground text-xs animate-fade-up">
              No runs yet. Click "Run Pipeline" to start.
            </div>
          ) : (
            <div className="space-y-1.5">
              {runsData?.data?.map((run: PipelineRun, i: number) => (
                <Link
                  key={run.id}
                  href={`/pipelines/${pipelineId}/runs/${run.id}`}
                  className="group flex items-center justify-between border border-border/50 rounded-lg px-4 py-3 hover:bg-accent/25 hover:border-primary/15 transition-all duration-150 animate-fade-up"
                  style={{ animationDelay: `${i * 25}ms` }}
                >
                  <div className="flex items-center gap-3">
                    <StatusBadge visual={pipelineRunStatus(run.status)} />
                    <span className="text-xs text-muted-foreground">
                      Stage {run.current_stage + 1}/{run.total_stages}
                    </span>
                  </div>
                  <div className="flex items-center gap-3">
                    <span className="text-[11px] text-muted-foreground/70">
                      {formatDuration(run.started_at, run.finished_at)}
                    </span>
                    <span className="text-[11px] text-muted-foreground/70">
                      {new Date(run.created_at).toLocaleString()}
                    </span>
                    <ChevronRight className="w-3.5 h-3.5 text-muted-foreground/40 group-hover:text-primary/60 transition-colors" />
                  </div>
                </Link>
              ))}
            </div>
          )}
          {runsData && runsData.total > 20 && (
            <div className="mt-4">
              <Pagination page={page} perPage={20} total={runsData.total} onPageChange={setPage} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function PipelineVariablesTab({ pipelineId }: { pipelineId: string }) {
  const uid = useId();
  const queryClient = useQueryClient();
  const confirm = useConfirm();
  const { user } = useAuth();
  // A pipeline variable reaches the worker's tfvars file and process
  // environment for every stage in the pipeline, so the API holds the writes at
  // ActionManageVars — admin, org-scoped, the same bar org and workspace
  // variables sit at. Reads stay open with sensitive values redacted.
  const canManage = roleAtLeast(user?.role, 'admin');
  const [showForm, setShowForm] = useState(false);
  const [editTarget, setEditTarget] = useState<string | null>(null);
  const [editValue, setEditValue] = useState('');
  const [editSensitive, setEditSensitive] = useState(false);
  const [editDescription, setEditDescription] = useState('');
  const [editCategory, setEditCategory] = useState<'terraform' | 'env'>('terraform');
  const [newKey, setNewKey] = useState('');
  const [newValue, setNewValue] = useState('');
  const [newSensitive, setNewSensitive] = useState(false);
  const [newCategory, setNewCategory] = useState<'terraform' | 'env'>('terraform');
  const [newDescription, setNewDescription] = useState('');

  const { data: variables } = useQuery({
    queryKey: ['pipeline-variables', pipelineId],
    queryFn: async () => {
      const { data, error } = await api.GET('/pipelines/{pipelineId}/variables', {
        params: { path: { pipelineId } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/pipelines/{pipelineId}/variables', {
        params: { path: { pipelineId } },
        body: {
          key: newKey,
          value: newValue,
          sensitive: newSensitive,
          category: newCategory,
          description: newDescription,
        },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['pipeline-variables', pipelineId] });
      toast.success('Variable created');
      setNewKey('');
      setNewValue('');
      setNewSensitive(false);
      setNewCategory('terraform');
      setNewDescription('');
      setShowForm(false);
    },
    onError: () => toast.error('Failed to create variable'),
  });

  const updateMutation = useMutation({
    mutationFn: async (variableId: string) => {
      const { data, error } = await api.PUT('/pipelines/{pipelineId}/variables/{variableId}', {
        params: { path: { pipelineId, variableId } },
        body: {
          key: variables?.find((v: PipelineVariable) => v.id === variableId)?.key ?? '',
          value: editValue,
          sensitive: editSensitive,
          category: editCategory,
          description: editDescription,
        },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['pipeline-variables', pipelineId] });
      toast.success('Variable updated');
      setEditTarget(null);
    },
    onError: () => toast.error('Failed to update variable'),
  });

  const deleteMutation = useMutation({
    mutationFn: async (variableId: string) => {
      const { error } = await api.DELETE('/pipelines/{pipelineId}/variables/{variableId}', {
        params: { path: { pipelineId, variableId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['pipeline-variables', pipelineId] });
      toast.success('Variable deleted');
    },
    onError: () => toast.error('Failed to delete variable'),
  });

  const startEdit = (v: PipelineVariable) => {
    setEditTarget(v.id);
    setEditValue(v.sensitive ? '' : v.value);
    setEditSensitive(v.sensitive);
    setEditDescription(v.description);
    setEditCategory(v.category as 'terraform' | 'env');
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <p className="text-xs text-muted-foreground">
          Variables applied to all stages in this pipeline. Workspace-level values override these.
          {!canManage && ' Editing them is an admin action.'}
        </p>
        {canManage && (
          <Button size="sm" onClick={() => setShowForm(true)}>
            <Plus className="w-3.5 h-3.5" />
            Add Variable
          </Button>
        )}
      </div>

      {showForm && (
        <div className="border border-border/60 rounded-lg p-4 mb-4 space-y-2 bg-accent/10 animate-fade-up">
          <div className="grid grid-cols-[1fr_auto] gap-2">
            <Input placeholder="Key" value={newKey} onChange={(e) => setNewKey(e.target.value)} />
            <Select
              value={newCategory}
              onChange={(e) => setNewCategory(e.target.value as 'terraform' | 'env')}
            >
              <option value="terraform">Terraform</option>
              <option value="env">Environment</option>
            </Select>
          </div>
          <Input
            placeholder="Value"
            type={newSensitive ? 'password' : 'text'}
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
          />
          <Input
            placeholder="Description (optional)"
            value={newDescription}
            onChange={(e) => setNewDescription(e.target.value)}
          />
          <div className="flex items-center gap-4">
            <label
              htmlFor={`${uid}-new-sensitive`}
              className="flex items-center gap-2 text-sm cursor-pointer"
            >
              <input
                id={`${uid}-new-sensitive`}
                type="checkbox"
                checked={newSensitive}
                onChange={(e) => setNewSensitive(e.target.checked)}
                className="rounded border-border"
              />
              Sensitive
            </label>
            <div className="flex-1" />
            <Button size="sm" variant="ghost" onClick={() => setShowForm(false)}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => createMutation.mutate()}
              disabled={!newKey || createMutation.isPending}
            >
              {createMutation.isPending ? <Spinner /> : 'Create'}
            </Button>
          </div>
        </div>
      )}

      {variables && variables.length === 0 && !showForm ? (
        <div className="text-center py-12 text-muted-foreground text-xs">
          No pipeline variables yet.
        </div>
      ) : (
        <div className="rounded-lg border border-border/60 divide-y divide-border/40">
          {variables?.map((v: PipelineVariable) =>
            editTarget === v.id ? (
              <div key={v.id} className="px-4 py-3 space-y-2 bg-accent/20">
                <code className="text-sm font-mono font-medium">{v.key}</code>
                <div className="grid grid-cols-[1fr_auto] gap-2">
                  <Input
                    placeholder={v.sensitive ? 'Enter new value' : 'Value'}
                    type={editSensitive ? 'password' : 'text'}
                    value={editValue}
                    onChange={(e) => setEditValue(e.target.value)}
                  />
                  <Select
                    value={editCategory}
                    onChange={(e) => setEditCategory(e.target.value as 'terraform' | 'env')}
                  >
                    <option value="terraform">Terraform</option>
                    <option value="env">Environment</option>
                  </Select>
                </div>
                <Input
                  placeholder="Description (optional)"
                  value={editDescription}
                  onChange={(e) => setEditDescription(e.target.value)}
                />
                <div className="flex items-center gap-4">
                  <label
                    htmlFor={`${uid}-edit-sensitive`}
                    className="flex items-center gap-2 text-sm cursor-pointer"
                  >
                    <input
                      id={`${uid}-edit-sensitive`}
                      type="checkbox"
                      checked={editSensitive}
                      onChange={(e) => setEditSensitive(e.target.checked)}
                      className="rounded border-border"
                    />
                    Sensitive
                  </label>
                  <div className="flex-1" />
                  <Button size="sm" variant="ghost" onClick={() => setEditTarget(null)}>
                    Cancel
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => updateMutation.mutate(v.id)}
                    disabled={updateMutation.isPending}
                  >
                    {updateMutation.isPending ? <Spinner /> : 'Save'}
                  </Button>
                </div>
              </div>
            ) : (
              <div key={v.id} className="flex items-center justify-between px-4 py-3">
                <div>
                  <div className="flex items-center gap-3">
                    <code className="text-sm font-mono font-medium">{v.key}</code>
                    <Badge variant="outline" className="text-xs">
                      {v.category}
                    </Badge>
                    {v.sensitive && <Lock className="w-3.5 h-3.5 text-muted-foreground" />}
                  </div>
                  {v.description && (
                    <p className="text-xs text-muted-foreground mt-0.5">{v.description}</p>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-sm font-mono text-muted-foreground break-all">
                    {v.sensitive ? '***' : v.value}
                  </span>
                  {canManage && (
                    <>
                      <button
                        onClick={() => startEdit(v)}
                        className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                      >
                        <Pencil className="w-3.5 h-3.5" />
                      </button>
                      <button
                        onClick={async () => {
                          if (await confirm({ title: `Delete ${v.key}?`, confirmLabel: 'Delete' }))
                            deleteMutation.mutate(v.id);
                        }}
                        className="p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors cursor-pointer"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </>
                  )}
                </div>
              </div>
            ),
          )}
        </div>
      )}
    </div>
  );
}
