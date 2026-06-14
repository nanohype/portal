import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Team, TeamMember, User } from "@/api/types";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { formatRelativeTime } from "@/lib/utils";
import { Plus, Users, Trash2, ChevronRight, X, Pencil, Check } from "lucide-react";
import { toast } from "sonner";

export function TeamsPage() {
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [teamName, setTeamName] = useState("");
  const [selectedTeam, setSelectedTeam] = useState<Team | null>(null);

  const { data: teams, isLoading, isError } = useQuery({
    queryKey: ["teams"],
    queryFn: async () => {
      const { data, error } = await api.GET("/teams");
      if (error) throw error;
      return data;
    },
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/teams", {
        body: { name: teamName },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["teams"] });
      setTeamName("");
      setShowCreate(false);
      toast.success("Team created");
    },
    onError: () => toast.error("Failed to create team"),
  });

  const deleteMutation = useMutation({
    mutationFn: async (teamId: string) => {
      const { error } = await api.DELETE("/teams/{teamId}", {
        params: { path: { teamId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["teams"] });
      setSelectedTeam(null);
      toast.success("Team deleted");
    },
    onError: () => toast.error("Failed to delete team"),
  });

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Teams</h1>
          <p className="text-[12px] text-muted-foreground mt-1">
            Manage team membership and workspace access.
          </p>
        </div>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="w-4 h-4" />
          Create team
        </Button>
      </div>

      {/* Create team dialog */}
      <Dialog open={showCreate} onClose={() => setShowCreate(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create team</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <Input
              placeholder="Team name"
              value={teamName}
              onChange={(e) => setTeamName(e.target.value)}
              autoFocus
            />
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setShowCreate(false)}>
                Cancel
              </Button>
              <Button
                onClick={() => createMutation.mutate()}
                disabled={!teamName || createMutation.isPending}
              >
                {createMutation.isPending ? <Spinner /> : "Create"}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* Team detail dialog */}
      {selectedTeam && (
        <Dialog open={!!selectedTeam} onClose={() => setSelectedTeam(null)}>
          <DialogContent className="max-w-xl">
            <DialogHeader>
              <DialogTitle>{selectedTeam.name}</DialogTitle>
            </DialogHeader>
            <TeamDetail
              team={selectedTeam}
              onDelete={() => deleteMutation.mutate(selectedTeam.id)}
            />
          </DialogContent>
        </Dialog>
      )}

      {/* Teams list */}
      {isLoading ? (
        <div className="flex-1 flex items-center justify-center">
          <Spinner className="w-6 h-6" />
        </div>
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-10 text-center">
            <p className="text-sm text-destructive">Failed to load teams. Please try again.</p>
          </div>
        </div>
      ) : !teams?.length ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-dashed border-border p-10 text-center">
            <Users className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
            <h3 className="font-medium mb-1">No teams yet</h3>
            <p className="text-sm text-muted-foreground mb-4">
              Create a team to organize workspace access.
            </p>
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="w-3.5 h-3.5" />
              Create team
            </Button>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-border divide-y divide-border">
          {(teams as Team[]).map((team) => (
            <button
              key={team.id}
              onClick={() => setSelectedTeam(team)}
              className="w-full flex items-center justify-between px-4 py-3 hover:bg-accent/50 transition-colors text-left cursor-pointer"
            >
              <div className="flex items-center gap-3">
                <Users className="w-4 h-4 text-muted-foreground" />
                <div>
                  <div className="font-medium text-sm">{team.name}</div>
                  <div className="text-xs text-muted-foreground">
                    @{team.slug} &middot; Created{" "}
                    {formatRelativeTime(team.created_at)}
                  </div>
                </div>
              </div>
              <ChevronRight className="w-4 h-4 text-muted-foreground" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function TeamDetail({
  team,
  onDelete,
}: {
  team: Team;
  onDelete: () => void;
}) {
  const queryClient = useQueryClient();

  const { data: members, isLoading } = useQuery({
    queryKey: ["team-members", team.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/teams/{teamId}/members", {
        params: { path: { teamId: team.id } },
      });
      if (error) throw error;
      return data;
    },
  });

  const [showAddMember, setShowAddMember] = useState(false);
  const [addMemberUserId, setAddMemberUserId] = useState("");
  const [addMemberRole, setAddMemberRole] = useState("viewer");
  const [addMemberIdentity, setAddMemberArn] = useState("");

  // Edit state
  const [editMemberId, setEditMemberId] = useState<string | null>(null);
  const [editRole, setEditRole] = useState("");
  const [editIdentity, setEditArn] = useState("");

  const { data: users } = useQuery({
    queryKey: ["users"],
    queryFn: async () => {
      const { data, error } = await api.GET("/users");
      if (error) throw error;
      return data as User[];
    },
    enabled: showAddMember,
  });

  const addMemberMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/teams/{teamId}/members", {
        params: { path: { teamId: team.id } },
        body: { user_id: addMemberUserId, role: addMemberRole, cloud_identity: addMemberIdentity },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team-members", team.id] });
      setShowAddMember(false);
      setAddMemberUserId("");
      setAddMemberRole("viewer");
      setAddMemberArn("");
      toast.success("Member added");
    },
    onError: () => toast.error("Failed to add member"),
  });

  const updateMemberMutation = useMutation({
    mutationFn: async (userId: string) => {
      const { data, error } = await api.PUT("/teams/{teamId}/members/{userId}", {
        params: { path: { teamId: team.id, userId } },
        body: { role: editRole, cloud_identity: editIdentity },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team-members", team.id] });
      setEditMemberId(null);
      toast.success("Member updated");
    },
    onError: () => toast.error("Failed to update member"),
  });

  const removeMemberMutation = useMutation({
    mutationFn: async (userId: string) => {
      const { error } = await api.DELETE("/teams/{teamId}/members/{userId}", {
        params: { path: { teamId: team.id, userId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team-members", team.id] });
      toast.success("Member removed");
    },
    onError: () => toast.error("Failed to remove member"),
  });

  const startEdit = (m: TeamMember) => {
    setEditMemberId(m.user_id);
    setEditRole(m.role);
    setEditArn(m.cloud_identity || "");
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Badge variant="outline">@{team.slug}</Badge>
        <span>Created {formatRelativeTime(team.created_at)}</span>
      </div>

      <div>
        <div className="flex items-center justify-between mb-2">
          <h4 className="text-sm font-medium">Members</h4>
          <Button size="sm" variant="outline" onClick={() => setShowAddMember(!showAddMember)}>
            <Plus className="w-3 h-3" />
            Add member
          </Button>
        </div>

        {showAddMember && (
          <div className="mb-3 p-3 rounded-lg border border-border bg-accent/20 space-y-2">
            <Select value={addMemberUserId} onChange={(e) => setAddMemberUserId(e.target.value)}>
              <option value="">Select user...</option>
              {(users || [])
                .filter((u: User) => !(members as TeamMember[] || []).some((m) => m.user_id === u.id))
                .map((u: User) => (
                  <option key={u.id} value={u.id}>{u.name} ({u.email})</option>
                ))}
            </Select>
            <div className="grid grid-cols-[auto_1fr] gap-2">
              <Select value={addMemberRole} onChange={(e) => setAddMemberRole(e.target.value)}>
                <option value="viewer">Viewer</option>
                <option value="operator">Operator</option>
                <option value="admin">Admin</option>
              </Select>
              <Input
                placeholder="Cloud identity (ARN, SA email, principal ID) (optional)"
                value={addMemberIdentity}
                onChange={(e) => setAddMemberArn(e.target.value)}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setShowAddMember(false)}>Cancel</Button>
              <Button size="sm" onClick={() => addMemberMutation.mutate()} disabled={!addMemberUserId || addMemberMutation.isPending}>
                {addMemberMutation.isPending ? <Spinner /> : "Add"}
              </Button>
            </div>
          </div>
        )}
        {isLoading ? (
          <Spinner className="w-4 h-4" />
        ) : !members?.length ? (
          <p className="text-sm text-muted-foreground">No members yet.</p>
        ) : (
          <div className="space-y-2">
            {(members as TeamMember[]).map((m) =>
              editMemberId === m.user_id ? (
                <div key={m.id} className="p-3 rounded-lg border border-primary/20 bg-accent/20 space-y-2">
                  <div className="flex items-center gap-2">
                    {m.avatar_url ? (
                      <img src={m.avatar_url} alt="" className="w-6 h-6 rounded-full" />
                    ) : (
                      <div className="w-6 h-6 rounded-full bg-primary/20 flex items-center justify-center text-xs">
                        {(m.user_name || m.email || "?")[0]}
                      </div>
                    )}
                    <span className="text-sm font-medium">{m.user_name || m.email}</span>
                  </div>
                  <div className="grid grid-cols-[auto_1fr] gap-2">
                    <Select value={editRole} onChange={(e) => setEditRole(e.target.value)}>
                      <option value="viewer">Viewer</option>
                      <option value="operator">Operator</option>
                      <option value="admin">Admin</option>
                    </Select>
                    <Input
                      placeholder="Cloud identity (ARN, SA email, principal ID)"
                      value={editIdentity}
                      onChange={(e) => setEditArn(e.target.value)}
                    />
                  </div>
                  <div className="flex justify-end gap-2">
                    <Button size="sm" variant="ghost" onClick={() => setEditMemberId(null)}>Cancel</Button>
                    <Button size="sm" onClick={() => updateMemberMutation.mutate(m.user_id)} disabled={updateMemberMutation.isPending}>
                      {updateMemberMutation.isPending ? <Spinner /> : <><Check className="w-3 h-3" /> Save</>}
                    </Button>
                  </div>
                </div>
              ) : (
                <div
                  key={m.id}
                  className="flex items-center gap-3 p-2 rounded bg-accent/30"
                >
                  {m.avatar_url ? (
                    <img src={m.avatar_url} alt="" className="w-6 h-6 rounded-full" />
                  ) : (
                    <div className="w-6 h-6 rounded-full bg-primary/20 flex items-center justify-center text-xs">
                      {(m.user_name || m.email || "?")[0]}
                    </div>
                  )}
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium">
                      {m.user_name || m.email}
                    </div>
                    <div className="text-xs text-muted-foreground">{m.email}</div>
                    {m.cloud_identity && (
                      <div className="text-[11px] text-muted-foreground/70 font-mono break-all mt-0.5">
                        {m.cloud_identity}
                      </div>
                    )}
                  </div>
                  <Badge variant="outline" className="text-xs shrink-0">
                    {m.role}
                  </Badge>
                  <button
                    onClick={() => startEdit(m)}
                    className="p-1 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
                    aria-label={`Edit ${m.user_name || m.email}`}
                  >
                    <Pencil className="w-3.5 h-3.5" />
                  </button>
                  <button
                    onClick={() => removeMemberMutation.mutate(m.user_id)}
                    disabled={removeMemberMutation.isPending}
                    className="p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors cursor-pointer"
                    aria-label={`Remove ${m.user_name || m.email}`}
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                </div>
              )
            )}
          </div>
        )}
      </div>

      <div className="pt-2 border-t border-border">
        <Button
          size="sm"
          variant="outline"
          className="text-destructive hover:bg-destructive/10"
          onClick={onDelete}
        >
          <Trash2 className="w-3.5 h-3.5" />
          Delete team
        </Button>
      </div>
    </div>
  );
}
