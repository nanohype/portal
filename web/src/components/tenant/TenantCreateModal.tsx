import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Cluster } from "@/api/types";
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

const K8S_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

// Personas mirrored from charts/tenant/values.yaml. Keeping the list here
// (rather than fetching from the chart at runtime) trades chart-change
// freshness for form responsiveness; if EAP adds a persona we update both
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
    setClusterID("");
    setName("");
    setDisplayName("");
    setTenant("");
    setPersona("generic");
    setMonthlyUsd(500);
    setHipaa(false);
    setSoc2(true);
  };

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/tenants", {
        body: {
          cluster_id: clusterID,
          name,
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
        },
      });
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
  const canSubmit =
    clusterID !== "" &&
    K8S_NAME_RE.test(name) &&
    monthlyUsd > 0 &&
    !mutation.isPending;

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>New Tenant</DialogTitle>
          <DialogDescription>
            Renders the EAP `charts/tenant` chart with these values and
            commits to the tenants repo. ArgoCD reconciles the result onto
            the chosen cluster.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
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

          <Field label="Display name (optional)">
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Marketing Team"
            />
          </Field>

          <Field label="Parent Tenant (defaults to platform name)">
            <Input
              value={tenant}
              onChange={(e) => setTenant(e.target.value)}
              placeholder="leave blank to use platform name"
              className="font-mono"
            />
          </Field>

          <Field label="Persona">
            <Select
              value={persona}
              onChange={(e) =>
                setPersona(e.target.value as (typeof PERSONAS)[number])
              }
            >
              {PERSONAS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Monthly Budget (USD)">
            <Input
              type="number"
              min={1}
              value={monthlyUsd}
              onChange={(e) =>
                setMonthlyUsd(Math.max(0, Number(e.target.value) || 0))
              }
              className="font-mono"
            />
          </Field>

          <Field label="Compliance">
            <div className="flex items-center gap-4 text-xs">
              <label className="inline-flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={soc2}
                  onChange={(e) => setSoc2(e.target.checked)}
                />
                SOC 2
              </label>
              <label className="inline-flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={hipaa}
                  onChange={(e) => setHipaa(e.target.checked)}
                />
                HIPAA
              </label>
            </div>
          </Field>

          <div className="flex justify-end gap-2 pt-3 border-t border-border/40">
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
  error,
  children,
}: {
  label: string;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
      </label>
      {children}
      {error && (
        <p className="text-[11px] text-destructive mt-1">{error}</p>
      )}
    </div>
  );
}
