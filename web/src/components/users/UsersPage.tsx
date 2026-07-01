import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { User } from "@/api/models";
import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { useAuth } from "@/hooks/useAuth";
import { formatRelativeTime } from "@/lib/utils";
import { UserCog } from "lucide-react";

const ROLES = ["owner", "admin", "operator", "viewer"] as const;

const roleColor = (role: string) => {
  switch (role) {
    case "owner":
      return "text-destructive border-destructive/30";
    case "admin":
      return "text-orange-500 border-orange-500/30";
    case "operator":
      return "text-blue-500 border-blue-500/30";
    default:
      return "text-muted-foreground border-border";
  }
};

export function UsersPage() {
  const queryClient = useQueryClient();
  const { user: currentUser } = useAuth();
  const isOwner = currentUser?.role === "owner";

  const { data: users, isLoading, isError } = useQuery({
    queryKey: ["users"],
    queryFn: async () => {
      const { data, error } = await api.GET("/users");
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const updateRoleMutation = useMutation({
    mutationFn: async ({ userId, role }: { userId: string; role: string }) => {
      const { data, error } = await api.PUT("/users/{userId}/role", {
        params: { path: { userId } },
        body: { role: role as "owner" | "admin" | "operator" | "viewer" },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["users"] });
      toast.success("Role updated");
    },
    onError: () => toast.error("Failed to update role"),
  });

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="mb-6">
        <h1 className="text-lg font-semibold tracking-tight">Users</h1>
        <p className="text-[12px] text-muted-foreground mt-1">
          Manage organization members and roles.
        </p>
      </div>

      {isLoading ? (
        <div className="flex-1 flex items-center justify-center">
          <Spinner className="w-6 h-6" />
        </div>
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-10 text-center">
            <p className="text-sm text-destructive">Failed to load users. Please try again.</p>
          </div>
        </div>
      ) : !users?.length ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="rounded-lg border border-dashed border-border p-10 text-center">
            <UserCog className="w-10 h-10 text-muted-foreground mx-auto mb-3" />
            <h3 className="font-medium mb-1">No users found</h3>
            <p className="text-sm text-muted-foreground">
              Users will appear here after they sign in.
            </p>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-border divide-y divide-border">
          {(users as User[]).map((u) => (
            <div
              key={u.id}
              className="flex items-center justify-between px-4 py-3"
            >
              <div className="flex items-center gap-3">
                {u.avatar_url ? (
                  <img
                    src={u.avatar_url}
                    alt={u.name}
                    className="w-8 h-8 rounded-full"
                  />
                ) : (
                  <div className="w-8 h-8 rounded-full bg-primary/20 flex items-center justify-center text-sm font-medium">
                    {u.name?.[0] ?? "?"}
                  </div>
                )}
                <div>
                  <div className="text-sm font-medium">{u.name}</div>
                  <div className="text-xs text-muted-foreground">
                    {u.email} &middot; Joined{" "}
                    {formatRelativeTime(u.created_at)}
                  </div>
                </div>
              </div>

              <div className="flex items-center gap-2">
                {isOwner ? (
                  <Select
                    value={u.role}
                    onChange={(e) =>
                      updateRoleMutation.mutate({
                        userId: u.id,
                        role: e.target.value,
                      })
                    }
                    className="w-32"
                  >
                    {ROLES.map((role) => (
                      <option key={role} value={role}>
                        {role.charAt(0).toUpperCase() + role.slice(1)}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Badge variant="outline" className={roleColor(u.role)}>
                    {u.role}
                  </Badge>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
