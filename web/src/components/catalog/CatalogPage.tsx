import { useState, useMemo, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import type {
  Workspace,
  Tenant,
  Pipeline,
  Cluster,
  Account,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatRelativeTime, getEnvironmentColor } from "@/lib/utils";
import {
  Boxes,
  FolderGit2,
  GitBranch,
  Search,
  Server,
  Cloud,
  LayoutGrid,
} from "lucide-react";

// Catalog renders every entity a user can see as a unified, filterable grid.
// Each entity-list endpoint already filters by the caller's role + team
// access, so this page automatically respects the existing visibility model
// without doing its own permission checks — it just merges what each list
// returns.

type CatalogKind =
  | "workspace"
  | "tenant"
  | "pipeline"
  | "cluster"
  | "account";

type CatalogEntry = {
  id: string;
  kind: CatalogKind;
  name: string;
  description: string;
  href: string;
  // Per-kind metadata rendered in the card. Optional so each kind can fill
  // what's relevant without forcing a uniform shape on entities that
  // don't have it.
  environment?: string;
  status?: string;
  statusVariant?: "success" | "default" | "destructive" | "warning" | "secondary";
  meta?: string;
  updatedAt?: string;
};

const KIND_META: Record<
  CatalogKind,
  { label: string; icon: React.ComponentType<{ className?: string }> }
> = {
  workspace: { label: "Workspaces", icon: FolderGit2 },
  tenant: { label: "Tenants", icon: Boxes },
  pipeline: { label: "Pipelines", icon: GitBranch },
  cluster: { label: "Clusters", icon: Server },
  account: { label: "Accounts", icon: Cloud },
};

export function CatalogPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";

  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [activeKind, setActiveKind] = useState<CatalogKind | "all">("all");

  // Debounce search to keep filtering snappy as the user types
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search), 200);
    return () => clearTimeout(t);
  }, [search]);

  const workspaces = useQuery({
    queryKey: ["catalog", "workspaces"],
    queryFn: async () => {
      const { data, error } = await api.GET("/workspaces", {
        params: { query: { per_page: 200 } },
      });
      if (error) throw error;
      return data!.data;
    },
  });

  const tenants = useQuery({
    queryKey: ["catalog", "tenants"],
    queryFn: async () => {
      const { data, error } = await api.GET("/tenants", {
        params: { query: { per_page: 200 } },
      });
      if (error) throw error;
      return data!.data;
    },
  });

  const pipelines = useQuery({
    queryKey: ["catalog", "pipelines"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines");
      if (error) throw error;
      return data!;
    },
  });

  // Clusters + Accounts are admin-managed infra. Skip the fetch for
  // non-admins both to save an API call and to avoid showing an empty
  // "Clusters" filter chip that they can't act on.
  const clusters = useQuery({
    queryKey: ["catalog", "clusters"],
    queryFn: async () => {
      const { data, error } = await api.GET("/clusters", {
        params: { query: { per_page: 200 } },
      });
      if (error) throw error;
      return data!.data;
    },
    enabled: isAdmin,
  });

  const accounts = useQuery({
    queryKey: ["catalog", "accounts"],
    queryFn: async () => {
      const { data, error } = await api.GET("/accounts", {
        params: { query: { per_page: 200 } },
      });
      if (error) throw error;
      return data!.data;
    },
    enabled: isAdmin,
  });

  // Merge each kind into a uniform CatalogEntry shape so the grid + filter
  // stage doesn't need to fan out by kind. Memoized on the underlying
  // query data so reflows stay cheap.
  const entries: CatalogEntry[] = useMemo(() => {
    const out: CatalogEntry[] = [];
    for (const w of (workspaces.data ?? []) as Workspace[]) {
      out.push({
        id: `workspace:${w.id}`,
        kind: "workspace",
        name: w.name,
        description: w.description ?? "",
        href: `/workspaces/${w.id}`,
        environment: w.environment,
        updatedAt: w.updated_at,
      });
    }
    for (const t of (tenants.data ?? []) as Tenant[]) {
      const phase = t.phase || "unknown";
      out.push({
        id: `tenant:${t.id}`,
        kind: "tenant",
        name: t.name,
        description: "",
        href: `/tenants/${t.id}`,
        status: phase,
        statusVariant: tenantPhaseVariant(phase),
        meta: t.cluster_id,
        updatedAt: t.last_observed_at,
      });
    }
    for (const p of (pipelines.data ?? []) as Pipeline[]) {
      out.push({
        id: `pipeline:${p.id}`,
        kind: "pipeline",
        name: p.name,
        description: p.description ?? "",
        href: `/pipelines/${p.id}`,
        updatedAt: p.updated_at,
      });
    }
    for (const c of (clusters.data ?? []) as Cluster[]) {
      out.push({
        id: `cluster:${c.id}`,
        kind: "cluster",
        name: c.name,
        description: c.description ?? "",
        href: `/clusters/${c.id}`,
        environment: c.environment,
        status: c.connection_status,
        statusVariant:
          c.connection_status === "connected"
            ? "success"
            : c.connection_status === "failed"
            ? "destructive"
            : "default",
        meta: c.region,
        updatedAt: c.updated_at,
      });
    }
    for (const a of (accounts.data ?? []) as Account[]) {
      out.push({
        id: `account:${a.id}`,
        kind: "account",
        name: a.name,
        description: a.description ?? "",
        href: `/accounts/${a.id}`,
        meta: `${a.aws_account_id} · ${a.default_region}`,
        updatedAt: a.updated_at,
      });
    }
    return out;
  }, [workspaces.data, tenants.data, pipelines.data, clusters.data, accounts.data]);

  const counts = useMemo(() => {
    const c: Record<CatalogKind, number> = {
      workspace: 0,
      tenant: 0,
      pipeline: 0,
      cluster: 0,
      account: 0,
    };
    for (const e of entries) c[e.kind]++;
    return c;
  }, [entries]);

  const filtered = useMemo(() => {
    const q = debouncedSearch.trim().toLowerCase();
    return entries.filter((e) => {
      if (activeKind !== "all" && e.kind !== activeKind) return false;
      if (q && !e.name.toLowerCase().includes(q) && !e.description.toLowerCase().includes(q)) return false;
      return true;
    });
  }, [entries, debouncedSearch, activeKind]);

  const anyLoading =
    workspaces.isLoading ||
    tenants.isLoading ||
    pipelines.isLoading ||
    (isAdmin && (clusters.isLoading || accounts.isLoading));

  const availableKinds: CatalogKind[] = isAdmin
    ? ["workspace", "tenant", "pipeline", "cluster", "account"]
    : ["workspace", "tenant", "pipeline"];

  return (
    <div className="p-6">
      <div className="mb-6">
        <h1 className="text-lg font-semibold tracking-tight">Catalog</h1>
        <p className="text-[12px] text-muted-foreground mt-1">
          Everything tofui knows you can see, in one place.
        </p>
      </div>

      {/* Stats summary */}
      <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 gap-2 mb-5">
        {availableKinds.map((k) => {
          const Icon = KIND_META[k].icon;
          return (
            <button
              key={k}
              onClick={() =>
                setActiveKind(activeKind === k ? "all" : k)
              }
              className={`group flex items-center gap-3 border rounded-lg px-3 py-2.5 transition-all duration-150 cursor-pointer text-left ${
                activeKind === k
                  ? "border-primary/30 bg-primary/[0.04]"
                  : "border-border/60 hover:bg-accent/30 hover:border-primary/15"
              }`}
            >
              <div className="w-8 h-8 rounded-md bg-primary/8 flex items-center justify-center shrink-0">
                <Icon className="w-3.5 h-3.5 text-primary/70" />
              </div>
              <div className="min-w-0">
                <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  {KIND_META[k].label}
                </div>
                <div className="text-sm font-semibold font-mono">
                  {counts[k]}
                </div>
              </div>
            </button>
          );
        })}
      </div>

      {/* Filter bar */}
      <div className="flex items-center gap-2 mb-4">
        <div className="relative flex-1 max-w-md">
          <Search className="w-3.5 h-3.5 text-muted-foreground/60 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search name or description…"
            className="pl-8"
          />
        </div>
        <button
          onClick={() => setActiveKind("all")}
          className={`text-[11px] px-2.5 py-1 rounded-md font-medium transition-colors cursor-pointer ${
            activeKind === "all"
              ? "bg-primary/10 text-primary"
              : "text-muted-foreground hover:bg-hover"
          }`}
        >
          all kinds
        </button>
      </div>

      {anyLoading && filtered.length === 0 ? (
        <div className="flex justify-center py-20">
          <Spinner className="w-6 h-6 text-primary" />
        </div>
      ) : filtered.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-20 text-center">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <LayoutGrid className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No matches</h2>
          <p className="text-xs text-muted-foreground max-w-[320px]">
            {debouncedSearch
              ? `Nothing matches "${debouncedSearch}".`
              : "Nothing here yet. Create a workspace, tenant, or pipeline to populate the catalog."}
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
          {filtered.map((e, i) => (
            <CatalogCard key={e.id} entry={e} index={i} />
          ))}
        </div>
      )}
    </div>
  );
}

function CatalogCard({ entry, index }: { entry: CatalogEntry; index: number }) {
  const Icon = KIND_META[entry.kind].icon;
  return (
    <Link
      href={entry.href}
      className="group block border border-border/60 rounded-lg px-4 py-3.5 hover:bg-accent/30 hover:border-primary/15 transition-all duration-150 animate-fade-up"
      style={{ animationDelay: `${Math.min(index, 20) * 25}ms` }}
    >
      <div className="flex items-start gap-3 mb-2">
        <div className="w-9 h-9 rounded-lg bg-primary/8 flex items-center justify-center shrink-0">
          <Icon className="w-4 h-4 text-primary/70" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-sm font-medium group-hover:text-primary transition-colors break-words">
              {entry.name}
            </span>
            <Badge variant="secondary">{entry.kind}</Badge>
            {entry.status && (
              <Badge variant={entry.statusVariant ?? "secondary"}>
                {entry.status}
              </Badge>
            )}
            {entry.environment && (
              <Badge variant="secondary" className={getEnvironmentColor(entry.environment)}>
                {entry.environment}
              </Badge>
            )}
          </div>
          {entry.description && (
            <p className="text-[11px] text-muted-foreground mt-1 break-words line-clamp-2">
              {entry.description}
            </p>
          )}
        </div>
      </div>
      {(entry.meta || entry.updatedAt) && (
        <div className="flex items-center gap-2 mt-1 text-[10px] text-muted-foreground/70 font-mono">
          {entry.meta && <span className="break-all">{entry.meta}</span>}
          {entry.meta && entry.updatedAt && <span>·</span>}
          {entry.updatedAt && <span>{formatRelativeTime(entry.updatedAt)}</span>}
        </div>
      )}
    </Link>
  );
}

function tenantPhaseVariant(
  phase: string,
): "success" | "default" | "destructive" | "warning" | "secondary" {
  switch (phase.toLowerCase()) {
    case "ready":
    case "active":
    case "healthy":
      return "success";
    case "pending":
    case "provisioning":
      return "default";
    case "error":
    case "failed":
    case "degraded":
      return "destructive";
    default:
      return "secondary";
  }
}
