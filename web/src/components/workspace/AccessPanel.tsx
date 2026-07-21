import { useId, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { WorkspaceTeamAccess, Team } from '@/api/models';
import { Button } from '@/components/ui/button';
import { Select } from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { Spinner } from '@/components/ui/spinner';
import { Users, Plus, Shield, X } from 'lucide-react';
import { roleAtLeast } from '@/lib/roles';
import { useAuth } from '@/hooks/useAuth';

interface Props {
  workspaceId: string;
}

export function AccessPanel({ workspaceId }: Props) {
  const { user } = useAuth();
  // Handing a team access to a workspace is an org-admin act, so this reads the
  // org role rather than the effective role on this workspace: holding a grant
  // here does not let you widen it. Showing the controls to anyone else offers
  // a button whose only outcome is a 403.
  const canManage = roleAtLeast(user?.role, 'admin');
  const uid = useId();
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [selectedTeamId, setSelectedTeamId] = useState('');
  const [selectedRole, setSelectedRole] = useState('viewer');

  const { data: accessList, isLoading } = useQuery({
    queryKey: ['workspace-access', workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET('/workspaces/{workspaceId}/access', {
        params: { path: { workspaceId } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const { data: teams } = useQuery({
    queryKey: ['teams'],
    queryFn: async () => {
      const { data, error } = await api.GET('/teams');
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: showForm,
  });

  const grantMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/workspaces/{workspaceId}/access', {
        params: { path: { workspaceId } },
        body: { team_id: selectedTeamId, role: selectedRole },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['workspace-access', workspaceId],
      });
      toast.success('Team access granted');
      setShowForm(false);
      setSelectedTeamId('');
      setSelectedRole('viewer');
    },
    onError: () => toast.error('Failed to grant access'),
  });

  const revokeMutation = useMutation({
    mutationFn: async (teamId: string) => {
      const { error } = await api.DELETE('/workspaces/{workspaceId}/access/{teamId}', {
        params: { path: { workspaceId, teamId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ['workspace-access', workspaceId],
      });
      toast.success('Team access revoked');
    },
    onError: () => toast.error('Failed to revoke access'),
  });

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Spinner className="w-5 h-5" />
      </div>
    );
  }

  const existingTeamIds = new Set(
    (accessList as WorkspaceTeamAccess[] | undefined)?.map((a) => a.team_id) ?? [],
  );
  const availableTeams = (teams as Team[] | undefined)?.filter((t) => !existingTeamIds.has(t.id));

  const roleColor = (role: string) => {
    switch (role) {
      case 'owner':
        return 'text-destructive border-destructive/30';
      case 'admin':
        return 'text-orange-500 border-orange-500/30';
      case 'operator':
        return 'text-blue-500 border-blue-500/30';
      default:
        return 'text-muted-foreground border-border';
    }
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-sm font-medium text-muted-foreground">Team Access</h3>
        {canManage && !showForm && (
          <Button size="sm" variant="outline" onClick={() => setShowForm(true)}>
            <Plus className="w-3.5 h-3.5" />
            Grant access
          </Button>
        )}
      </div>

      {canManage && showForm && (
        <div className="rounded-lg border border-border p-4 mb-4 space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label
                htmlFor={`${uid}-team`}
                className="text-xs font-medium text-muted-foreground mb-1 block"
              >
                Team
              </label>
              <Select
                id={`${uid}-team`}
                value={selectedTeamId}
                onChange={(e) => setSelectedTeamId(e.target.value)}
              >
                <option value="">Select a team</option>
                {availableTeams?.map((team) => (
                  <option key={team.id} value={team.id}>
                    {team.name}
                  </option>
                ))}
              </Select>
            </div>
            <div>
              <label
                htmlFor={`${uid}-role`}
                className="text-xs font-medium text-muted-foreground mb-1 block"
              >
                Role
              </label>
              <Select
                id={`${uid}-role`}
                value={selectedRole}
                onChange={(e) => setSelectedRole(e.target.value)}
              >
                <option value="viewer">Viewer</option>
                <option value="operator">Operator</option>
                <option value="admin">Admin</option>
                <option value="owner">Owner</option>
              </Select>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              onClick={() => grantMutation.mutate()}
              disabled={!selectedTeamId || grantMutation.isPending}
            >
              {grantMutation.isPending ? <Spinner /> : null}
              Grant
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setShowForm(false);
                setSelectedTeamId('');
                setSelectedRole('viewer');
              }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {!(accessList as WorkspaceTeamAccess[] | undefined)?.length ? (
        <div className="rounded-lg border border-dashed border-border p-10 text-center">
          <Users className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
          <h3 className="font-medium mb-1">No teams have access</h3>
          <p className="text-sm text-muted-foreground">
            {canManage
              ? 'Grant team access to control who can manage this workspace.'
              : 'An admin can grant a team access to this workspace.'}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border border-border divide-y divide-border">
          {(accessList as WorkspaceTeamAccess[]).map((access) => (
            <div key={access.id} className="flex items-center justify-between px-4 py-3">
              <div className="flex items-center gap-3">
                <Shield className="w-4 h-4 text-muted-foreground" />
                <div>
                  <span className="text-sm font-medium">{access.team_name}</span>
                  <span className="text-xs text-muted-foreground ml-2 font-mono">
                    {access.team_slug}
                  </span>
                </div>
              </div>
              <div className="flex items-center gap-2">
                <Badge variant="outline" className={roleColor(access.role)}>
                  {access.role}
                </Badge>
                {canManage && (
                  <button
                    onClick={() => revokeMutation.mutate(access.team_id)}
                    disabled={revokeMutation.isPending}
                    className="p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors cursor-pointer"
                    aria-label={`Revoke ${access.team_name} access`}
                  >
                    <X className="w-3.5 h-3.5" />
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
