import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { OrgVariable } from '@/api/models';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { Spinner } from '@/components/ui/spinner';
import { useConfirm } from '@/components/ui/confirm-context';
import { Pencil, Trash2, Plus, Lock, Eye, EyeOff, Settings } from 'lucide-react';

export function OrgSettings() {
  const queryClient = useQueryClient();
  const confirm = useConfirm();
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
  const [revealedValues, setRevealedValues] = useState<Record<string, string>>({});

  const {
    data: variables,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ['org-variables'],
    queryFn: async () => {
      const { data, error } = await api.GET('/variables');
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/variables', {
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
      queryClient.invalidateQueries({ queryKey: ['org-variables'] });
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
      const { data, error } = await api.PUT('/variables/{variableId}', {
        params: { path: { variableId } },
        body: {
          key: variables?.find((v: OrgVariable) => v.id === variableId)?.key ?? '',
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
      queryClient.invalidateQueries({ queryKey: ['org-variables'] });
      toast.success('Variable updated');
      setEditTarget(null);
    },
    onError: () => toast.error('Failed to update variable'),
  });

  const deleteMutation = useMutation({
    mutationFn: async (variableId: string) => {
      const { error } = await api.DELETE('/variables/{variableId}', {
        params: { path: { variableId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['org-variables'] });
      toast.success('Variable deleted');
    },
    onError: () => toast.error('Failed to delete variable'),
  });

  const startEdit = (v: OrgVariable) => {
    setEditTarget(v.id);
    setEditValue(v.sensitive ? '' : v.value);
    setEditSensitive(v.sensitive);
    setEditDescription(v.description);
    setEditCategory(v.category as 'terraform' | 'env');
  };

  const toggleReveal = async (v: OrgVariable) => {
    if (revealedValues[v.id]) {
      setRevealedValues((prev) => {
        const n = { ...prev };
        delete n[v.id];
        return n;
      });
      return;
    }
    const { data, error } = await api.GET('/variables/{variableId}/value', {
      params: { path: { variableId: v.id } },
    });
    if (error) {
      toast.error('Failed to reveal value');
      return;
    }
    setRevealedValues((prev) => ({ ...prev, [v.id]: data!.value }));
  };

  if (isLoading)
    return (
      <div className="flex justify-center py-20">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );

  return (
    <div className="p-6 animate-fade-up">
      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-3">
          <div className="w-9 h-9 rounded-lg bg-primary/10 flex items-center justify-center">
            <Settings className="w-4 h-4 text-primary" />
          </div>
          <div>
            <h1 className="text-lg font-semibold tracking-tight">Settings</h1>
            <p className="text-xs text-muted-foreground mt-0.5">
              Default variables inherited by all workspaces. Workspace-level values override these.
            </p>
          </div>
        </div>
        <Button size="sm" onClick={() => setShowForm(true)}>
          <Plus className="w-3.5 h-3.5" />
          Add Variable
        </Button>
      </div>

      {isError && (
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm mb-4">
          Failed to load variables.
        </div>
      )}

      {/* Create form */}
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
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
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

      {/* Variable list */}
      {variables && variables.length === 0 && !showForm ? (
        <div className="text-center py-16 text-muted-foreground text-xs animate-fade-up">
          No organization variables yet. These will be inherited by all workspaces.
        </div>
      ) : (
        <div className="rounded-lg border border-border/60 divide-y divide-border/40">
          {variables?.map((v: OrgVariable) =>
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
                  <label className="flex items-center gap-2 text-sm cursor-pointer">
                    <input
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
                  {v.sensitive && (
                    <button
                      onClick={() => toggleReveal(v)}
                      className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                    >
                      {revealedValues[v.id] ? (
                        <EyeOff className="w-3.5 h-3.5" />
                      ) : (
                        <Eye className="w-3.5 h-3.5" />
                      )}
                    </button>
                  )}
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
                </div>
              </div>
            ),
          )}
        </div>
      )}
    </div>
  );
}
