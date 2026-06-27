import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import type { Cluster, Template } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

// Maps a form-level state var to the dotted helm-values path it controls.
// The handler uses this allowlist to decide whether a field is editable for
// a template-driven submission: a field whose path isn't in
// template.allowed_overrides is disabled and inherits the template default.
const FIELD_PATHS = {
  monthlyUsd: "budget.monthlyUsd",
  persona: "platform.persona",
  displayName: "platform.displayName",
  tenant: "platform.tenant",
  soc2: "platform.compliance.soc2",
  hipaa: "platform.compliance.hipaa",
} as const;

const K8S_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

// Personas mirrored from charts/tenant/values.yaml. Keeping the list here
// (rather than fetching from the chart at runtime) trades chart-change
// freshness for form responsiveness; if eks-agent-platform adds a persona we update both
// the chart and this list together.
const PERSONAS = [
  "generic",
  "sales-ops",
  "support",
  "finance",
  "ops",
  "founder",
  "eng",
  "marketing",
  "legal",
] as const;

export function TenantCreateModal({
  open,
  onClose,
  clusters,
}: {
  open: boolean;
  onClose: () => void;
  clusters: Cluster[];
}) {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";

  const { data: templates } = useQuery({
    queryKey: ["templates"],
    queryFn: async () => {
      const { data, error } = await api.GET("/templates");
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: open,
  });

  // Teams the user can pick as the owning team for the new tenant. Admins
  // see all org teams (optional pick = no ownership = admin-only visibility);
  // non-admins see only their own teams (server rejects bad picks anyway).
  const { data: pickableTeams } = useQuery({
    queryKey: ["teams", isAdmin ? "all" : "mine"],
    queryFn: async () => {
      const { data, error } = await api.GET("/teams", {
        params: isAdmin ? {} : { query: { member_of: "me" } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: open,
  });

  // Mode: "template" (operators default) vs "scratch" (admin advanced).
  // When templates exist + user picks one, fields outside its
  // allowed_overrides are disabled and inherit template defaults.
  const [templateID, setTemplateID] = useState<string>("");
  const [scratchMode, setScratchMode] = useState(false);
  const selected = templates?.find((t) => t.id === templateID);

  const [owningTeamID, setOwningTeamID] = useState("");
  const [clusterID, setClusterID] = useState("");
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [tenant, setTenant] = useState("");
  const [persona, setPersona] =
    useState<(typeof PERSONAS)[number]>("generic");
  const [monthlyUsd, setMonthlyUsd] = useState(500);
  const [hipaa, setHipaa] = useState(false);
  const [soc2, setSoc2] = useState(true);

  const reset = () => {
    setTemplateID("");
    setScratchMode(false);
    setOwningTeamID("");
    setClusterID("");
    setName("");
    setDisplayName("");
    setTenant("");
    setPersona("generic");
    setMonthlyUsd(500);
    setHipaa(false);
    setSoc2(true);
  };

  // When a template is picked, prefill from its defaults so the form shows
  // what the operator is about to commit. Allowed-override fields stay
  // editable; everything else is shown disabled so they can see (but not
  // change) the value.
  useEffect(() => {
    if (!selected) return;
    const d = selected.default_values as Record<string, unknown> | undefined;
    const platform = (d?.platform ?? {}) as Record<string, unknown>;
    const compliance = (platform.compliance ?? {}) as Record<string, unknown>;
    const budget = (d?.budget ?? {}) as Record<string, unknown>;
    if (typeof platform.persona === "string") {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional prefill of editable fields from the picked template's defaults
      setPersona(platform.persona as (typeof PERSONAS)[number]);
    } else {
      setPersona(selected.persona as (typeof PERSONAS)[number]);
    }
    if (typeof platform.displayName === "string") setDisplayName(platform.displayName);
    if (typeof platform.tenant === "string") setTenant(platform.tenant);
    if (typeof budget.monthlyUsd === "number") setMonthlyUsd(budget.monthlyUsd);
    if (typeof compliance.soc2 === "boolean") setSoc2(compliance.soc2);
    if (typeof compliance.hipaa === "boolean") setHipaa(compliance.hipaa);
  }, [selected]);

  const allowed = (path: string) => {
    if (scratchMode || !selected) return true;
    return selected.allowed_overrides.includes(path);
  };

  // Auto-resolve owning team for the common case (operator in exactly one
  // team). They still see the picker so they know what's happening, but it
  // pre-selects so they don't have to click.
  useEffect(() => {
    if (!isAdmin && pickableTeams && pickableTeams.length === 1 && !owningTeamID) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional auto-resolve of the single owning team; the user can still override
      setOwningTeamID(pickableTeams[0].id);
    }
  }, [isAdmin, pickableTeams, owningTeamID]);

  // Build the payload's `values` differently in the two modes:
  // - Template mode: only send overrides for fields that differ from
  //   template defaults AND are in allowed_overrides. The server applies
  //   them on top of template.default_values.
  // - Scratch mode: send the full values blob.
  const buildBody = () => {
    if (selected && !scratchMode) {
      // Null-prototype accumulator: even if a guard were bypassed, there is no
      // prototype to pollute (paired with the __proto__/constructor/prototype
      // segment rejection below).
      const overrides: Record<string, Record<string, unknown>> = Object.create(
        null,
      ) as Record<string, Record<string, unknown>>;
      const setOverride = (path: string, value: unknown) => {
        if (!allowed(path)) return;
        const segments = path.split(".");
        // Never let a path segment reach Object.prototype (prototype pollution).
        if (
          segments.some(
            (s) => s === "__proto__" || s === "constructor" || s === "prototype",
          )
        )
          return;
        let cur: Record<string, unknown> = overrides;
        for (let i = 0; i < segments.length - 1; i++) {
          const seg = segments[i];
          if (!(seg in cur) || typeof cur[seg] !== "object")
            cur[seg] = Object.create(null) as Record<string, unknown>;
          cur = cur[seg] as Record<string, unknown>;
        }
        cur[segments[segments.length - 1]] = value;
      };
      setOverride(FIELD_PATHS.monthlyUsd, monthlyUsd);
      setOverride(FIELD_PATHS.persona, persona);
      setOverride(FIELD_PATHS.displayName, displayName || name);
      setOverride(FIELD_PATHS.tenant, tenant || name);
      setOverride(FIELD_PATHS.soc2, soc2);
      setOverride(FIELD_PATHS.hipaa, hipaa);
      return {
        cluster_id: clusterID,
        name,
        template_id: selected.id,
        owning_team_id: owningTeamID || undefined,
        values: overrides,
      };
    }
    return {
      cluster_id: clusterID,
      name,
      owning_team_id: owningTeamID || undefined,
      values: {
        platform: {
          name,
          tenant: tenant || name,
          persona,
          displayName: displayName || name,
          compliance: { hipaa, soc2 },
        },
        budget: { monthlyUsd },
      },
    };
  };

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/tenants", { body: buildBody() });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tenants"] });
      toast.success("Tenant create enqueued · ArgoCD will reconcile shortly");
      reset();
      onClose();
    },
    onError: (e: unknown) => {
      const msg =
        (e as { message?: string })?.message ?? "Failed to enqueue tenant";
      toast.error(msg);
    },
  });

  const nameInvalid = name !== "" && !K8S_NAME_RE.test(name);
  const budgetOverCap =
    selected && selected.max_budget_usd > 0 && monthlyUsd > selected.max_budget_usd;
  const needsTeamPick =
    !isAdmin && pickableTeams !== undefined && pickableTeams.length > 1 && owningTeamID === "";
  const noTeams = !isAdmin && pickableTeams !== undefined && pickableTeams.length === 0;
  const canSubmit =
    clusterID !== "" &&
    K8S_NAME_RE.test(name) &&
    monthlyUsd > 0 &&
    !budgetOverCap &&
    !needsTeamPick &&
    !noTeams &&
    !mutation.isPending &&
    (scratchMode || templates?.length === 0 || selected !== undefined);

  return (
    <Dialog open={open} onClose={onClose} size="xl">
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>New Tenant</DialogTitle>
          <DialogDescription>
            Renders the eks-agent-platform `charts/tenant` chart with these values and
            commits to the tenants repo. ArgoCD reconciles the result onto
            the chosen cluster.
          </DialogDescription>
        </DialogHeader>

        <div className="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
          {templates && templates.length > 0 && (
            <Field label="Template" className="sm:col-span-2">
              <div className="flex items-center gap-2">
                <Select
                  value={templateID}
                  onChange={(e) => {
                    setTemplateID(e.target.value);
                    setScratchMode(false);
                  }}
                  disabled={scratchMode}
                >
                  <option value="">Pick a template…</option>
                  {templates.map((t: Template) => (
                    <option key={t.id} value={t.id}>
                      {t.name} · {t.persona}
                      {t.max_budget_usd > 0 ? ` · ≤ $${t.max_budget_usd}/mo` : ""}
                    </option>
                  ))}
                </Select>
                {isAdmin && (
                  <button
                    type="button"
                    onClick={() => {
                      setScratchMode((s) => !s);
                      setTemplateID("");
                    }}
                    className="text-[11px] text-muted-foreground hover:text-foreground transition-colors cursor-pointer whitespace-nowrap"
                  >
                    {scratchMode ? "use template" : "from scratch"}
                  </button>
                )}
              </div>
              {selected && (
                <p className="text-[11px] text-muted-foreground/70 mt-1">
                  {selected.description || "Operator overrides limited to: "}
                  <span className="font-mono">
                    {selected.allowed_overrides.join(", ") || "none"}
                  </span>
                </p>
              )}
            </Field>
          )}

          {templates && templates.length === 0 && !isAdmin && (
            <div className="sm:col-span-2 bg-warning/10 text-warning text-[11px] rounded-md px-3 py-2">
              No templates have been defined yet. Ask an admin to set one up.
            </div>
          )}

          {noTeams && (
            <div className="sm:col-span-2 bg-warning/10 text-warning text-[11px] rounded-md px-3 py-2">
              You must belong to a team before creating tenants. Ask an admin
              to add you to one.
            </div>
          )}

          {pickableTeams && pickableTeams.length > 0 && (
            <Field
              label={isAdmin ? "Owning team (optional)" : "Owning team"}
              hint={
                isAdmin
                  ? "Leave blank for admin-only visibility."
                  : pickableTeams.length === 1
                  ? `Auto-assigned to your team: ${pickableTeams[0].name}`
                  : undefined
              }
            >
              <Select
                value={owningTeamID}
                onChange={(e) => setOwningTeamID(e.target.value)}
              >
                {isAdmin && <option value="">(no team — admin only)</option>}
                {pickableTeams.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}

          <Field label="Cluster">
            <Select
              value={clusterID}
              onChange={(e) => setClusterID(e.target.value)}
            >
              <option value="">Pick a cluster…</option>
              {clusters
                .filter((c) => c.connection_status === "connected")
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} ({c.region})
                  </option>
                ))}
            </Select>
            {clusters.filter((c) => c.connection_status === "connected")
              .length === 0 && (
              <p className="text-[11px] text-warning mt-1">
                No clusters are currently connected. Register one first.
              </p>
            )}
          </Field>

          <Field
            label="Platform name (k8s name)"
            error={
              nameInvalid
                ? "Lowercase alphanumeric + hyphen, 1-63 chars, must start/end alphanumeric"
                : null
            }
          >
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="marketing-team"
              className="font-mono"
            />
          </Field>

          <Field
            label="Display name (optional)"
            locked={!allowed(FIELD_PATHS.displayName)}
          >
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Marketing Team"
              disabled={!allowed(FIELD_PATHS.displayName)}
            />
          </Field>

          <Field
            label="Parent Tenant (defaults to platform name)"
            locked={!allowed(FIELD_PATHS.tenant)}
          >
            <Input
              value={tenant}
              onChange={(e) => setTenant(e.target.value)}
              placeholder="leave blank to use platform name"
              className="font-mono"
              disabled={!allowed(FIELD_PATHS.tenant)}
            />
          </Field>

          <Field label="Persona" locked={!allowed(FIELD_PATHS.persona)}>
            <Select
              value={persona}
              onChange={(e) =>
                setPersona(e.target.value as (typeof PERSONAS)[number])
              }
              disabled={!allowed(FIELD_PATHS.persona)}
            >
              {PERSONAS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </Field>

          <Field
            label="Monthly Budget (USD)"
            locked={!allowed(FIELD_PATHS.monthlyUsd)}
            error={budgetOverCap ? `Exceeds template cap of $${selected!.max_budget_usd}` : null}
            hint={
              selected && selected.max_budget_usd > 0
                ? `Template cap: $${selected.max_budget_usd.toLocaleString()}/mo`
                : undefined
            }
          >
            <Input
              type="number"
              min={1}
              value={monthlyUsd}
              onChange={(e) =>
                setMonthlyUsd(Math.max(0, Number(e.target.value) || 0))
              }
              className="font-mono"
              disabled={!allowed(FIELD_PATHS.monthlyUsd)}
            />
          </Field>

          <Field label="Compliance">
            <div className="flex items-center gap-4 text-xs">
              <label className="inline-flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={soc2}
                  onChange={(e) => setSoc2(e.target.checked)}
                  disabled={!allowed(FIELD_PATHS.soc2)}
                />
                SOC 2
                {selected?.required_compliance.includes("soc2") && (
                  <Badge variant="warning">required</Badge>
                )}
              </label>
              <label className="inline-flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={hipaa}
                  onChange={(e) => setHipaa(e.target.checked)}
                  disabled={!allowed(FIELD_PATHS.hipaa)}
                />
                HIPAA
                {selected?.required_compliance.includes("hipaa") && (
                  <Badge variant="warning">required</Badge>
                )}
              </label>
            </div>
          </Field>

          <div className="sm:col-span-2 flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => mutation.mutate()}
              disabled={!canSubmit}
            >
              {mutation.isPending ? "Enqueueing..." : "Create Tenant"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Field({
  label,
  hint,
  error,
  locked,
  className,
  children,
}: {
  label: string;
  hint?: string;
  error?: string | null;
  locked?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={className}>
      <label className="text-xs font-medium text-muted-foreground mb-1.5 flex items-center gap-1.5">
        {label}
        {locked && (
          <span className="text-[10px] text-muted-foreground/60 font-normal">
            (locked by template)
          </span>
        )}
      </label>
      {children}
      {error ? (
        <p className="text-[11px] text-destructive mt-1">{error}</p>
      ) : hint ? (
        <p className="text-[11px] text-muted-foreground/70 mt-1">{hint}</p>
      ) : null}
    </div>
  );
}
