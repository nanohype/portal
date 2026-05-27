import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import { phaseBadge } from "./TenantList";
import { ArrowLeft, Boxes, ChevronDown, ChevronRight } from "lucide-react";

export function TenantDetail({ tenantId }: { tenantId: string }) {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["tenant", tenantId],
    queryFn: async () => {
      const { data, error } = await api.GET("/tenants/{tenantId}", {
        params: { path: { tenantId } },
      });
      if (error) throw error;
      return data!;
    },
    refetchInterval: 10000,
  });

  const [showSpec, setShowSpec] = useState(false);
  const [showStatus, setShowStatus] = useState(true);

  if (isLoading) {
    return (
      <div className="flex justify-center py-20">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="p-6">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load tenant.
        </div>
      </div>
    );
  }

  return (
    <div className="p-6 animate-fade-up">
      <Link
        href="/tenants"
        className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 mb-3 transition-colors"
      >
        <ArrowLeft className="w-3 h-3" />
        Tenants
      </Link>

      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-primary/8 flex items-center justify-center">
            <Boxes className="w-4 h-4 text-primary/70" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-lg font-semibold tracking-tight">
                {data.name}
              </h1>
              {phaseBadge(data.phase)}
            </div>
            <p className="text-[12px] text-muted-foreground mt-0.5">
              <Link
                href={`/clusters/${data.cluster_id}`}
                className="hover:text-foreground"
              >
                cluster {data.cluster_id}
              </Link>
              {" · "}observed {formatRelativeTime(data.last_observed_at)}
            </p>
          </div>
        </div>
      </div>

      <p className="text-[11px] text-muted-foreground/70 mb-6">
        Read-only inventory. Tenant CRUD via git (phase 2c) is not yet wired.
      </p>

      <JSONPanel
        title="Status"
        open={showStatus}
        onToggle={() => setShowStatus(!showStatus)}
        value={data.status}
      />

      <JSONPanel
        title="Spec"
        open={showSpec}
        onToggle={() => setShowSpec(!showSpec)}
        value={data.spec}
      />
    </div>
  );
}

function JSONPanel({
  title,
  open,
  onToggle,
  value,
}: {
  title: string;
  open: boolean;
  onToggle: () => void;
  value: unknown;
}) {
  return (
    <div className="mb-4 border border-border/60 rounded-lg overflow-hidden">
      <button
        type="button"
        onClick={onToggle}
        className="w-full flex items-center gap-2 px-4 py-2.5 text-xs font-medium hover:bg-accent/30 transition-colors cursor-pointer"
      >
        {open ? (
          <ChevronDown className="w-3 h-3" />
        ) : (
          <ChevronRight className="w-3 h-3" />
        )}
        {title}
      </button>
      {open && (
        <pre className="px-4 py-3 text-[11px] font-mono whitespace-pre-wrap break-words bg-background/40 border-t border-border/40 max-h-[500px] overflow-auto">
          {JSON.stringify(value, null, 2)}
        </pre>
      )}
    </div>
  );
}
