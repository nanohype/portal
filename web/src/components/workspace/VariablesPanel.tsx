import { useId, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type {
  WorkspaceVariable,
  DiscoveredVariable,
  EffectiveVariable,
  Workspace,
  ListResponse,
} from '@/api/models';
import { Button } from '@/components/ui/button';
import { roleAtLeast } from '@/lib/roles';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { Spinner } from '@/components/ui/spinner';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog';
import { TagEditor } from '@/components/ui/tag-editor';
import {
  Plus,
  Trash2,
  Lock,
  Search,
  X,
  Check,
  Pencil,
  Eye,
  EyeOff,
  Upload,
  ArrowDownToLine,
  Copy,
  Layers,
} from 'lucide-react';

function isTagsKey(key: string) {
  return key === 'tags' || key === 'default_tags' || key === 'extra_tags' || key.endsWith('_tags');
}

interface Props {
  workspaceId: string;
  // The caller's effective role on this workspace — their org role, or a
  // higher role one of their teams was granted here.
  role?: string;
}

function parseEnvFormat(text: string): { key: string; value: string }[] {
  const result: { key: string; value: string }[] = [];
  for (const line of text.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const eqIdx = trimmed.indexOf('=');
    if (eqIdx <= 0) continue;
    const key = trimmed.slice(0, eqIdx).trim();
    let value = trimmed.slice(eqIdx + 1).trim();
    // Strip surrounding quotes
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    result.push({ key, value });
  }
  return result;
}

export function VariablesPanel({ workspaceId, role }: Props) {
  // Variable writes feed the worker's tfvars file and process environment, so
  // the API holds them at admin. Reads stay open, with values redacted.
  const canManage = roleAtLeast(role, 'admin');
  // Handing back a decrypted secret is at least as sensitive as editing it, so
  // reveal sits at the same bar as the writes.
  const canReveal = canManage;
  // Discover parses the workspace's config and returns the variable names it
  // declares. It writes nothing, so it sits on the read bar — the step that
  // tells an operator what to fill in has to be reachable by one.
  const canDiscover = roleAtLeast(role, 'viewer');
  const uid = useId();
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [newKey, setNewKey] = useState('');
  const [newValue, setNewValue] = useState('');
  const [newSensitive, setNewSensitive] = useState(false);
  const [newCategory, setNewCategory] = useState<'terraform' | 'env'>('terraform');
  const [newDescription, setNewDescription] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<WorkspaceVariable | null>(null);
  const [discoveredVars, setDiscoveredVars] = useState<DiscoveredVariable[] | null>(null);

  // Inline edit state
  const [editTarget, setEditTarget] = useState<string | null>(null);
  const [editValue, setEditValue] = useState('');
  const [editSensitive, setEditSensitive] = useState(false);
  const [editDescription, setEditDescription] = useState('');
  const [editCategory, setEditCategory] = useState<'terraform' | 'env'>('terraform');

  // Reveal state
  const [revealedValues, setRevealedValues] = useState<Record<string, string>>({});

  // Bulk import state
  const [showBulkImport, setShowBulkImport] = useState(false);
  const [bulkText, setBulkText] = useState('');
  const [bulkCategory, setBulkCategory] = useState<'terraform' | 'env'>('terraform');
  const [bulkSensitive, setBulkSensitive] = useState(false);
  const [bulkParsed, setBulkParsed] = useState<{ key: string; value: string }[] | null>(null);

  // Import outputs state
  const [showImportOutputs, setShowImportOutputs] = useState(false);
  const [importWorkspaces, setImportWorkspaces] = useState<Workspace[] | null>(null);

  // Copy variables state
  const [showCopyVars, setShowCopyVars] = useState(false);
  const [copyWorkspaces, setCopyWorkspaces] = useState<Workspace[] | null>(null);

  // Effective view
  const [showEffective, setShowEffective] = useState(false);

  const {
    data: variables,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ['variables', workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/variables', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const { data: effectiveVars } = useQuery({
    queryKey: ['effective-variables', workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/variables/effective', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data!;
    },
    enabled: showEffective,
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/variables', {
        params: { path: { workspaceId } },
        body: {
          key: newKey,
          value: newValue,
          sensitive: newSensitive,
          category: newCategory,
          description: newDescription,
        },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      setNewKey('');
      setNewValue('');
      setNewSensitive(false);
      setNewDescription('');
      setShowForm(false);
      toast.success('Variable created');
      if (discoveredVars) discoverMutation.mutate();
    },
    onError: () => toast.error('Failed to create variable'),
  });

  const updateMutation = useMutation({
    mutationFn: async (variableId: string) => {
      const { data, error } = await api.PUT('/workspaces/{workspaceId}/variables/{variableId}', {
        params: { path: { workspaceId, variableId } },
        body: {
          key: variables?.find((v: WorkspaceVariable) => v.id === variableId)?.key ?? '',
          value: editValue,
          sensitive: editSensitive,
          category: editCategory,
          description: editDescription,
        },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      setEditTarget(null);
      toast.success('Variable updated');
    },
    onError: () => toast.error('Failed to update variable'),
  });

  const deleteMutation = useMutation({
    mutationFn: async (variableId: string) => {
      const { error } = await api.DELETE('/workspaces/{workspaceId}/variables/{variableId}', {
        params: { path: { workspaceId, variableId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      setDeleteTarget(null);
      toast.success('Variable deleted');
    },
    onError: () => toast.error('Failed to delete variable'),
  });

  const discoverMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/variables/discover', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: (data) => setDiscoveredVars(data ?? []),
    onError: () => toast.error('Failed to discover variables'),
  });

  const bulkCreateMutation = useMutation({
    mutationFn: async (vars: { key: string; value: string }[]) => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/variables/bulk', {
        params: { path: { workspaceId } },
        body: {
          variables: vars.map((v) => ({
            key: v.key,
            value: v.value,
            sensitive: bulkSensitive,
            category: bulkCategory,
          })),
        },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      setShowBulkImport(false);
      setBulkText('');
      setBulkParsed(null);
      toast.success('Variables imported');
    },
    onError: () => toast.error('Failed to import variables'),
  });

  const addAllMutation = useMutation({
    mutationFn: async (vars: DiscoveredVariable[]) => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/variables/bulk', {
        params: { path: { workspaceId } },
        body: {
          variables: vars.map((v) => ({
            key: v.name,
            value: v.default ?? '',
            sensitive: false,
            category: 'terraform' as const,
            description: v.description,
          })),
        },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      discoverMutation.mutate();
      toast.success('All variables added');
    },
    onError: () => toast.error('Failed to add variables'),
  });

  const fetchWorkspacesMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.GET('/workspaces', {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: (data) => {
      const others = (data.data as Workspace[]).filter((w: Workspace) => w.id !== workspaceId);
      setImportWorkspaces(others);
      setShowImportOutputs(true);
    },
    onError: () => toast.error('Failed to load workspaces'),
  });

  const importOutputsMutation = useMutation({
    mutationFn: async (sourceWorkspaceId: string) => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/variables/import-outputs', {
        params: { path: { workspaceId } },
        body: { source_workspace_id: sourceWorkspaceId },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      setShowImportOutputs(false);
      setImportWorkspaces(null);
      const count = Array.isArray(data) ? data.length : 0;
      toast.success(`Imported ${count} output(s) as variables`);
    },
    onError: () => toast.error('Failed to import outputs'),
  });

  const copyVariablesMutation = useMutation({
    mutationFn: async (sourceWorkspaceId: string) => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/variables/copy', {
        params: { path: { workspaceId } },
        body: { source_workspace_id: sourceWorkspaceId },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['variables', workspaceId] });
      setShowCopyVars(false);
      setCopyWorkspaces(null);
      toast.success('Variables copied');
    },
    onError: () => toast.error('Failed to copy variables'),
  });

  const openCopyDialog = async () => {
    setShowCopyVars(true);
    try {
      const { data, error } = await api.GET('/workspaces', {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      const list = (data as ListResponse<Workspace>).data ?? [];
      setCopyWorkspaces(list.filter((w: Workspace) => w.id !== workspaceId));
    } catch {
      toast.error('Failed to load workspaces');
      setShowCopyVars(false);
    }
  };

  const handleAddDiscovered = (v: DiscoveredVariable) => {
    setNewKey(v.name);
    setNewValue(v.default ?? '');
    setNewCategory('terraform');
    setNewSensitive(false);
    setNewDescription(v.description ?? '');
    setShowForm(true);
  };

  const startEdit = (v: WorkspaceVariable) => {
    setEditTarget(v.id);
    setEditValue(v.sensitive ? '' : v.value);
    setEditSensitive(v.sensitive);
    setEditDescription(v.description);
    setEditCategory(v.category as 'terraform' | 'env');
  };

  const toggleReveal = async (v: WorkspaceVariable) => {
    if (revealedValues[v.id]) {
      setRevealedValues((prev) => {
        const n = { ...prev };
        delete n[v.id];
        return n;
      });
      return;
    }
    try {
      const { data, error } = await api.GET(
        '/workspaces/{workspaceId}/variables/{variableId}/value',
        { params: { path: { workspaceId, variableId: v.id } } },
      );
      if (error) throw error;
      setRevealedValues((prev) => ({ ...prev, [v.id]: data.value }));
    } catch {
      toast.error('Failed to reveal variable value');
    }
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
        <p className="text-sm text-destructive">Failed to load variables.</p>
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <h3 className="text-base font-semibold">Variables</h3>
          <button
            onClick={() => setShowEffective(!showEffective)}
            className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium transition-all cursor-pointer ${
              showEffective
                ? 'bg-primary/10 text-primary border border-primary/20'
                : 'text-muted-foreground hover:text-foreground hover:bg-accent/40 border border-transparent'
            }`}
          >
            <Layers className="w-3 h-3" />
            Effective
          </button>
        </div>
        <div className="flex items-center gap-2">
          {canDiscover && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => discoverMutation.mutate()}
              disabled={discoverMutation.isPending}
            >
              {discoverMutation.isPending ? (
                <Spinner className="w-3.5 h-3.5" />
              ) : (
                <Search className="w-3.5 h-3.5" />
              )}
              Discover
            </Button>
          )}
          {canManage && (
            <>
              <Button
                size="sm"
                variant="outline"
                onClick={() => fetchWorkspacesMutation.mutate()}
                disabled={fetchWorkspacesMutation.isPending}
              >
                {fetchWorkspacesMutation.isPending ? (
                  <Spinner className="w-3.5 h-3.5" />
                ) : (
                  <ArrowDownToLine className="w-3.5 h-3.5" />
                )}
                Import Outputs
              </Button>
              <Button size="sm" variant="outline" onClick={() => setShowBulkImport(true)}>
                <Upload className="w-3.5 h-3.5" />
                Bulk Import
              </Button>
              <Button size="sm" variant="outline" onClick={openCopyDialog}>
                <Copy className="w-3.5 h-3.5" />
                Copy Variables
              </Button>
              <Button size="sm" variant="outline" onClick={() => setShowForm(!showForm)}>
                <Plus className="w-3.5 h-3.5" />
                Add variable
              </Button>
            </>
          )}
        </div>
      </div>

      {/* Discovered variables panel */}
      {discoveredVars && (
        <div className="mb-4 rounded-lg border border-border bg-card">
          <div className="flex items-center justify-between px-4 py-3 border-b border-border">
            <div className="flex items-center gap-2">
              <Search className="w-4 h-4 text-muted-foreground" />
              <span className="text-sm font-medium">
                Discovered Variables ({discoveredVars.length})
              </span>
            </div>
            <div className="flex items-center gap-2">
              {discoveredVars.filter((v) => !v.configured).length > 0 && (
                <Button
                  size="sm"
                  variant="outline"
                  className="text-xs h-7"
                  disabled={addAllMutation.isPending}
                  onClick={() => addAllMutation.mutate(discoveredVars.filter((v) => !v.configured))}
                >
                  {addAllMutation.isPending ? (
                    <Spinner className="w-3 h-3" />
                  ) : (
                    <Plus className="w-3 h-3" />
                  )}
                  Add all ({discoveredVars.filter((v) => !v.configured).length})
                </Button>
              )}
              <button
                onClick={() => setDiscoveredVars(null)}
                className="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
          </div>
          {discoveredVars.length === 0 ? (
            <div className="px-4 py-6 text-center">
              <p className="text-sm text-muted-foreground">
                No variable blocks found in the repository.
              </p>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {discoveredVars.map((v) => (
                <div key={v.name} className="px-4 py-3">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3 min-w-0">
                      <code className="text-sm font-mono font-medium">{v.name}</code>
                      {v.type && (
                        <Badge variant="outline" className="text-xs shrink-0">
                          {v.type}
                        </Badge>
                      )}
                      {v.configured ? (
                        v.configured_by === 'terragrunt' ? (
                          <Badge className="text-xs bg-violet-500/10 text-violet-600 border-violet-500/20 shrink-0">
                            <Check className="w-3 h-3 mr-1" />
                            terragrunt
                          </Badge>
                        ) : (
                          <Badge className="text-xs bg-emerald-500/10 text-emerald-600 border-emerald-500/20 shrink-0">
                            <Check className="w-3 h-3 mr-1" />
                            configured
                          </Badge>
                        )
                      ) : v.required ? (
                        <Badge className="text-xs bg-red-500/10 text-red-600 border-red-500/20 shrink-0">
                          required
                        </Badge>
                      ) : (
                        <Badge className="text-xs bg-amber-500/10 text-amber-600 border-amber-500/20 shrink-0">
                          optional
                        </Badge>
                      )}
                      {v.default !== undefined && (
                        <span className="text-xs font-mono text-muted-foreground">
                          ={v.default}
                        </span>
                      )}
                    </div>
                    <div className="shrink-0 ml-3">
                      {/* Discovery is a read; creating the variable it found is
                          still a write, so the Add button keeps the write bar. */}
                      {!v.configured && canManage && (
                        <Button
                          size="sm"
                          variant="outline"
                          className="text-xs h-7"
                          onClick={() => handleAddDiscovered(v)}
                        >
                          <Plus className="w-3 h-3" />
                          Add
                        </Button>
                      )}
                    </div>
                  </div>
                  {(v.description || v.configured_by === 'terragrunt') && (
                    <p className="text-xs text-muted-foreground mt-1">
                      {v.description}
                      {v.configured_by === 'terragrunt' &&
                        (v.description
                          ? ' — set in terragrunt.hcl; edit there to change'
                          : 'set in terragrunt.hcl; edit there to change')}
                    </p>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Create form */}
      {showForm && (
        <div className="mb-4 p-4 rounded-lg border border-border bg-card space-y-3">
          <Input
            placeholder="Variable key"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
          />
          {isTagsKey(newKey) && newCategory === 'terraform' ? (
            <TagEditor value={newValue || '{}'} onChange={setNewValue} />
          ) : (
            <Input
              placeholder="Value"
              type={newSensitive ? 'password' : 'text'}
              value={newValue}
              onChange={(e) => setNewValue(e.target.value)}
            />
          )}
          <Input
            placeholder="Description (optional)"
            value={newDescription}
            onChange={(e) => setNewDescription(e.target.value)}
          />
          <div className="flex items-center gap-4">
            <Select
              value={newCategory}
              onChange={(e) => setNewCategory(e.target.value as 'terraform' | 'env')}
            >
              <option value="terraform">Terraform</option>
              <option value="env">Environment</option>
            </Select>
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
              {createMutation.isPending ? <Spinner /> : 'Save'}
            </Button>
          </div>
        </div>
      )}

      {/* Effective variables view */}
      {showEffective && effectiveVars && (
        <div className="mb-4 rounded-lg border border-primary/15 bg-primary/[0.02]">
          <div className="px-4 py-2 border-b border-primary/10">
            <span className="text-xs font-medium text-primary">
              Effective Variables (org + pipeline + workspace merged)
            </span>
          </div>
          <div className="divide-y divide-border/30">
            {effectiveVars.length === 0 ? (
              <div className="px-4 py-6 text-center text-xs text-muted-foreground">
                No variables from any scope.
              </div>
            ) : (
              effectiveVars.map((v: EffectiveVariable) => (
                <div
                  key={v.key + v.category}
                  className="flex items-center justify-between px-4 py-2.5"
                >
                  <div className="flex items-center gap-3">
                    <code className="text-sm font-mono font-medium">{v.key}</code>
                    <Badge variant="outline" className="text-xs">
                      {v.category}
                    </Badge>
                    <Badge
                      variant={
                        v.source === 'workspace'
                          ? 'default'
                          : v.source === 'pipeline'
                            ? 'warning'
                            : 'secondary'
                      }
                      className="text-[10px]"
                    >
                      {v.source}
                    </Badge>
                    {v.sensitive && <Lock className="w-3 h-3 text-muted-foreground" />}
                  </div>
                  <span className="text-sm font-mono text-muted-foreground break-all">
                    {v.sensitive ? '***' : v.value}
                  </span>
                </div>
              ))
            )}
          </div>
        </div>
      )}

      {/* Variable list */}
      {!variables?.length ? (
        <div className="rounded-lg border border-dashed border-border p-8 text-center">
          <p className="text-sm text-muted-foreground">No variables configured yet.</p>
        </div>
      ) : (
        <div className="rounded-lg border border-border divide-y divide-border">
          {variables.map((v: WorkspaceVariable) =>
            editTarget === v.id ? (
              <div key={v.id} className="px-4 py-3 space-y-2 bg-accent/20">
                <div className="flex items-center gap-2">
                  <code className="text-sm font-mono font-medium">{v.key}</code>
                </div>
                {isTagsKey(v.key) && editCategory === 'terraform' ? (
                  <TagEditor value={editValue} onChange={setEditValue} />
                ) : (
                  <div className="grid grid-cols-[1fr_auto] gap-2">
                    <Input
                      placeholder={v.sensitive ? 'Enter new value' : 'Value'}
                      type={editSensitive ? 'password' : 'text'}
                      value={editValue}
                      onChange={(e) => setEditValue(e.target.value)}
                    />
                  </div>
                )}
                <div className="flex gap-2">
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
                    {v.sensitive ? (revealedValues[v.id] ?? '***') : v.value}
                  </span>
                  {v.sensitive && canReveal && (
                    <button
                      onClick={() => toggleReveal(v)}
                      className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                      title={revealedValues[v.id] ? 'Hide value' : 'Reveal value'}
                    >
                      {revealedValues[v.id] ? (
                        <EyeOff className="w-3.5 h-3.5" />
                      ) : (
                        <Eye className="w-3.5 h-3.5" />
                      )}
                    </button>
                  )}
                  {canManage && (
                    <>
                      <button
                        onClick={() => startEdit(v)}
                        className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                        title="Edit variable"
                      >
                        <Pencil className="w-3.5 h-3.5" />
                      </button>
                      <button
                        onClick={() => setDeleteTarget(v)}
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

      {/* Delete dialog */}
      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete variable</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete{' '}
              <code className="font-mono font-medium">{deleteTarget?.key}</code>? This action cannot
              be undone.
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2 mt-4">
            <Button variant="ghost" size="sm" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
              }}
              disabled={deleteMutation.isPending}
            >
              {deleteMutation.isPending ? <Spinner /> : 'Delete'}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* Import outputs dialog */}
      <Dialog
        open={showImportOutputs}
        onClose={() => {
          setShowImportOutputs(false);
          setImportWorkspaces(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Import Outputs</DialogTitle>
            <DialogDescription>
              Import output values from another workspace as variables.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 mt-2 max-h-80 overflow-auto">
            {importWorkspaces?.length === 0 ? (
              <p className="text-sm text-muted-foreground text-center py-4">
                No other workspaces found.
              </p>
            ) : (
              importWorkspaces?.map((ws) => (
                <button
                  key={ws.id}
                  onClick={() => importOutputsMutation.mutate(ws.id)}
                  disabled={importOutputsMutation.isPending}
                  className="w-full flex items-center justify-between px-4 py-3 rounded-lg border border-border hover:border-primary/30 transition-colors text-left cursor-pointer"
                >
                  <div>
                    <div className="text-sm font-medium">{ws.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {ws.environment} &middot; {ws.source}
                    </div>
                  </div>
                  <ArrowDownToLine className="w-4 h-4 text-muted-foreground" />
                </button>
              ))
            )}
          </div>
        </DialogContent>
      </Dialog>

      {/* Copy variables dialog */}
      <Dialog
        open={showCopyVars}
        onClose={() => {
          setShowCopyVars(false);
          setCopyWorkspaces(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Copy Variables</DialogTitle>
            <DialogDescription>
              Select a workspace to copy variables from. Existing variables with the same key and
              category will be updated.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 mt-2">
            {!copyWorkspaces ? (
              <div className="flex items-center justify-center py-8">
                <Spinner className="w-5 h-5" />
              </div>
            ) : copyWorkspaces.length === 0 ? (
              <div className="py-6 text-center">
                <p className="text-sm text-muted-foreground">No other workspaces found.</p>
              </div>
            ) : (
              <div className="rounded-lg border border-border divide-y divide-border max-h-60 overflow-auto">
                {copyWorkspaces.map((w: Workspace) => (
                  <button
                    key={w.id}
                    className="flex items-center justify-between w-full px-4 py-3 text-left hover:bg-accent/50 transition-colors cursor-pointer"
                    onClick={() => copyVariablesMutation.mutate(w.id)}
                    disabled={copyVariablesMutation.isPending}
                  >
                    <div>
                      <p className="text-sm font-medium">{w.name}</p>
                      {w.description && (
                        <p className="text-xs text-muted-foreground mt-0.5">{w.description}</p>
                      )}
                    </div>
                    <Badge variant="outline" className="text-xs shrink-0">
                      {w.environment}
                    </Badge>
                  </button>
                ))}
              </div>
            )}
            <div className="flex justify-end gap-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setShowCopyVars(false);
                  setCopyWorkspaces(null);
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* Bulk import dialog */}
      <Dialog
        open={showBulkImport}
        onClose={() => {
          setShowBulkImport(false);
          setBulkParsed(null);
          setBulkText('');
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Bulk Import Variables</DialogTitle>
            <DialogDescription>
              Paste variables in .env format (KEY=value), one per line. Lines starting with # are
              ignored.
            </DialogDescription>
          </DialogHeader>
          {!bulkParsed ? (
            <div className="space-y-3 mt-2">
              <textarea
                className="w-full h-40 rounded-lg border border-border bg-background p-3 font-mono text-sm resize-none focus:outline-none focus:ring-2 focus:ring-primary/30"
                placeholder={
                  'DB_HOST=localhost\nDB_PORT=5432\n# Comment lines are ignored\nAPI_KEY=secret123'
                }
                value={bulkText}
                onChange={(e) => setBulkText(e.target.value)}
              />
              <div className="flex items-center gap-4">
                <Select
                  value={bulkCategory}
                  onChange={(e) => setBulkCategory(e.target.value as 'terraform' | 'env')}
                >
                  <option value="terraform">Terraform</option>
                  <option value="env">Environment</option>
                </Select>
                <label
                  htmlFor={`${uid}-bulk-sensitive`}
                  className="flex items-center gap-2 text-sm cursor-pointer"
                >
                  <input
                    id={`${uid}-bulk-sensitive`}
                    type="checkbox"
                    checked={bulkSensitive}
                    onChange={(e) => setBulkSensitive(e.target.checked)}
                    className="rounded border-border"
                  />
                  Sensitive
                </label>
              </div>
              <div className="flex justify-end gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setShowBulkImport(false);
                    setBulkText('');
                  }}
                >
                  Cancel
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    const parsed = parseEnvFormat(bulkText);
                    if (parsed.length === 0) {
                      toast.error('No valid variables found');
                      return;
                    }
                    setBulkParsed(parsed);
                  }}
                  disabled={!bulkText.trim()}
                >
                  Preview
                </Button>
              </div>
            </div>
          ) : (
            <div className="space-y-3 mt-2">
              <div className="rounded-lg border border-border divide-y divide-border max-h-60 overflow-auto">
                {bulkParsed.map((v, i) => (
                  <div key={i} className="flex items-center justify-between px-3 py-2">
                    <code className="text-sm font-mono">{v.key}</code>
                    <span className="text-sm font-mono text-muted-foreground break-all">
                      {bulkSensitive ? '***' : v.value}
                    </span>
                  </div>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">
                {bulkParsed.length} variable(s) will be created as {bulkCategory}
                {bulkSensitive ? ' (sensitive)' : ''}
              </p>
              <div className="flex justify-end gap-2">
                <Button variant="ghost" size="sm" onClick={() => setBulkParsed(null)}>
                  Back
                </Button>
                <Button
                  size="sm"
                  onClick={() => bulkCreateMutation.mutate(bulkParsed)}
                  disabled={bulkCreateMutation.isPending}
                >
                  {bulkCreateMutation.isPending ? (
                    <Spinner />
                  ) : (
                    `Import ${bulkParsed.length} variables`
                  )}
                </Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
