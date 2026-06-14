import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import { Cloud, Plus } from "lucide-react";
import { AccountCreateModal } from "./AccountCreateModal";

export function AccountList() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, isError } = useQuery({
    queryKey: ["accounts"],
    queryFn: async () => {
      const { data, error } = await api.GET("/accounts", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data!;
    },
  });

  const accounts = data?.data ?? [];

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Accounts</h1>
          <p className="text-[12px] text-muted-foreground mt-1">
            AWS accounts portal can operate against
          </p>
        </div>
        {isAdmin && (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="w-3.5 h-3.5" />
            New Account
          </Button>
        )}
      </div>

      {isLoading ? (
        <SkeletonRows />
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
            Failed to load accounts.
          </div>
        </div>
      ) : accounts.length === 0 ? (
        <div className="flex-1 flex flex-col items-center justify-center text-center animate-fade-up">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <Cloud className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No accounts yet</h2>
          <p className="text-xs text-muted-foreground mb-5 max-w-[300px]">
            Register an AWS account so portal can manage clusters and resources
            inside it.
          </p>
          {isAdmin && (
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="w-3.5 h-3.5" />
              Add Account
            </Button>
          )}
        </div>
      ) : (
        <div className="space-y-2">
          {accounts.map((a, i) => (
            <Link
              key={a.id}
              href={`/accounts/${a.id}`}
              className="group block border border-border/60 rounded-lg px-4 py-3.5 hover:bg-accent/30 hover:border-primary/15 transition-all duration-150 animate-fade-up"
              style={{ animationDelay: `${i * 30}ms` }}
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg bg-primary/8 flex items-center justify-center shrink-0">
                    <Cloud className="w-3.5 h-3.5 text-primary/70" />
                  </div>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium group-hover:text-primary transition-colors">
                        {a.name}
                      </span>
                      <span className="text-[11px] font-mono text-muted-foreground/70">
                        {a.aws_account_id}
                      </span>
                    </div>
                    {a.description && (
                      <p className="text-xs text-muted-foreground break-words mt-0.5">
                        {a.description}
                      </p>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-3 shrink-0 text-[11px] text-muted-foreground/70">
                  <span className="font-mono">{a.default_region}</span>
                  <span>{formatRelativeTime(a.created_at)}</span>
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}

      <AccountCreateModal
        open={showCreate}
        onClose={() => setShowCreate(false)}
      />
    </div>
  );
}
