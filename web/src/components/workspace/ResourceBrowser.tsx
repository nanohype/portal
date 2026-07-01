import { useState, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { StateResource } from "@/api/models";
import { Spinner } from "@/components/ui/spinner";
import { ChevronDown, ChevronRight, Search } from "lucide-react";

interface Props {
  workspaceId: string;
  enabled: boolean;
}

function ResourceRow({ resource }: { resource: StateResource }) {
  const [expanded, setExpanded] = useState(false);
  const attrEntries = Object.entries(resource.attributes).sort(([a], [b]) =>
    a.localeCompare(b)
  );

  return (
    <div className="border-b border-border last:border-b-0">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full px-4 py-2.5 flex items-center gap-2 text-sm font-mono text-left hover:bg-accent/50 transition-colors cursor-pointer"
      >
        {expanded ? (
          <ChevronDown className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
        ) : (
          <ChevronRight className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
        )}
        <span className="text-foreground font-medium">
          {resource.type}.{resource.name}
        </span>
        {resource.module && (
          <span className="text-xs text-muted-foreground">
            ({resource.module})
          </span>
        )}
        <span className="ml-auto text-xs text-muted-foreground">
          {resource.provider}
        </span>
      </button>
      {expanded && attrEntries.length > 0 && (
        <div className="px-4 pb-3 pt-1">
          <div className="rounded border border-border divide-y divide-border text-xs font-mono">
            {attrEntries.map(([key, value]) => (
              <div key={key} className="flex px-3 py-1.5">
                <span className="text-muted-foreground w-48 shrink-0">
                  {key}
                </span>
                <span className="text-foreground/80 break-all">
                  {typeof value === "object"
                    ? JSON.stringify(value)
                    : String(value ?? "null")}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

export function ResourceBrowser({ workspaceId, enabled }: Props) {
  const [search, setSearch] = useState("");

  const {
    data: resources,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ["state-resources", workspaceId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/workspaces/{workspaceId}/state/current/resources",
        { params: { path: { workspaceId } } }
      );
      if (error) throw error;
      return data as StateResource[];
    },
    enabled,
  });

  const filtered = useMemo(() => {
    if (!resources) return [];
    if (!search.trim()) return resources;
    const q = search.toLowerCase();
    return resources.filter(
      (r) =>
        r.type.toLowerCase().includes(q) ||
        r.name.toLowerCase().includes(q) ||
        r.module.toLowerCase().includes(q) ||
        r.provider.toLowerCase().includes(q)
    );
  }, [resources, search]);

  if (!enabled) return null;

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Spinner className="w-5 h-5" />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-6 text-center">
        <p className="text-sm text-destructive">
          Failed to load resources. Storage may not be configured.
        </p>
      </div>
    );
  }

  return (
    <div className="mt-4">
      <div className="relative mb-3">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
        <input
          type="text"
          placeholder="Filter resources by type, name, module..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full pl-9 pr-3 py-2 text-sm border border-border rounded-lg bg-background focus:outline-none focus:ring-2 focus:ring-ring"
        />
      </div>

      {filtered.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border p-6 text-center">
          <p className="text-sm text-muted-foreground">
            {resources?.length ? "No resources match your filter." : "No resources in state."}
          </p>
        </div>
      ) : (
        <div className="rounded-lg border border-border">
          <div className="px-4 py-2 bg-accent/30 text-xs text-muted-foreground font-medium border-b border-border">
            {filtered.length} resource{filtered.length !== 1 ? "s" : ""}
            {search && resources && filtered.length !== resources.length && (
              <span> (of {resources.length} total)</span>
            )}
          </div>
          {filtered.map((r, i) => (
            <ResourceRow
              key={`${r.module}.${r.type}.${r.name}-${i}`}
              resource={r}
            />
          ))}
        </div>
      )}
    </div>
  );
}
