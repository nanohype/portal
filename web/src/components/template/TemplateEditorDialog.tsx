import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Team, Template, TemplateTeamAccess } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { formatRelativeTime } from "@/lib/utils";
import { Trash2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

// Personas, model families, and compliance flags come from the eks-agent-platform chart's
// known taxonomies. Keeping these hardcoded here trades chart-evolution
// freshness for form responsiveness — when eks-agent-platform adds an option, bump it here
// and in the chart together.
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
];
const MODEL_FAMILIES = ["anthropic", "amazon-nova", "openai", "google"];
const COMPLIANCE_FLAGS = ["soc2", "hipaa"];

// Suggested override paths the admin can pick from when defining what
// operators can change. Matches the eks-agent-platform `charts/tenant` values schema.
const OVERRIDE_PATH_SUGGESTIONS = [
  "platform.displayName",
  "platform.tenant",
  "platform.compliance.hipaa",
  "platform.compliance.soc2",
  "budget.monthlyUsd",
  "identity.allowedModelFamilies",
];

export function TemplateEditorDialog({
  open,
  onClose,
  existing,
}: {
  open: boolean;
  onClose: () => void;
  existing: Template | null;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [persona, setPersona] = useState("generic");
  const [maxBudget, setMaxBudget] = useState(0);
  const [allowedFamilies, setAllowedFamilies] = useState<Set<string>>(
    new Set(),
  );
  const [requiredCompliance, setRequiredCompliance] = useState<Set<string>>(
    new Set(),
  );
  const [allowedOverrides, setAllowedOverrides] = useState<Set<string>>(
    new Set(),
  );
  const [defaultValuesYaml, setDefaultValuesYaml] = useState("");

  useEffect(() => {
    if (existing) {
      setName(existing.name);
      setDescription(existing.description);
      setPersona(existing.persona);
      setMaxBudget(existing.max_budget_usd);
      setAllowedFamilies(new Set(existing.allowed_model_families));
      setRequiredCompliance(new Set(existing.required_compliance));
      setAllowedOverrides(new Set(existing.allowed_overrides));
      setDefaultValuesYaml(
        JSON.stringify(existing.default_values ?? {}, null, 2),
      );
    } else {
      setName("");
      setDescription("");
      setPersona("generic");
      setMaxBudget(0);
      setAllowedFamilies(new Set());
      setRequiredCompliance(new Set());
      setAllowedOverrides(new Set());
      setDefaultValuesYaml("{}");
    }
  }, [existing, open]);

  const parseDefaults = (): {
    ok: boolean;
    parsed?: Record<string, unknown>;
    err?: string;
  } => {
    try {
      const obj = JSON.parse(defaultValuesYaml || "{}");
      if (typeof obj !== "object" || obj === null || Array.isArray(obj)) {
        return { ok: false, err: "default_values must be a JSON object" };
      }
      return { ok: true, parsed: obj as Record<string, unknown> };
    } catch (e) {
      return { ok: false, err: (e as Error).message };
    }
  };

  const mutation = useMutation({
    mutationFn: async () => {
      const defaults = parseDefaults();
      if (!defaults.ok) throw new Error(defaults.err);

      const body = {
        name: name.trim(),
        description: description.trim(),
        persona,
        default_values: defaults.parsed,
        allowed_overrides: Array.from(allowedOverrides),
        max_budget_usd: maxBudget,
        allowed_model_families: Array.from(allowedFamilies),
        required_compliance: Array.from(requiredCompliance),
      };
      if (existing) {
        const { data, error } = await api.PUT("/templates/{templateID}", {
          params: { path: { templateID: existing.id } },
          body,
        });
        if (error) throw error;
        return data!;
      }
      const { data, error } = await api.POST("/templates", { body });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["templates"] });
      toast.success(existing ? "Template updated" : "Template created");
      onClose();
    },
    onError: (e: unknown) => {
      const msg =
        (e as { message?: string })?.message ?? "Failed to save template";
      toast.error(msg);
    },
  });

  const defaultsErr = parseDefaults().err;
  const canSubmit =
    name.trim() !== "" && persona !== "" && !defaultsErr && !mutation.isPending;

  const toggleSet =
    (setter: React.Dispatch<React.SetStateAction<Set<string>>>) =>
    (value: string) =>
      setter((prev) => {
        const next = new Set(prev);
        if (next.has(value)) next.delete(value);
        else next.add(value);
        return next;
      });

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{existing ? "Edit Template" : "New Template"}</DialogTitle>
          <DialogDescription>
            Curate the defaults + caps. Operators instantiate tenants against
            this template and can only override the paths you allow.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Field label="Name">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="marketing-team"
              className="font-mono"
            />
          </Field>

          <Field label="Description">
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What's this template for?"
            />
          </Field>

          <Field label="Persona">
            <Select
              value={persona}
              onChange={(e) => setPersona(e.target.value)}
            >
              {PERSONAS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Max budget (USD/month) — 0 = no cap">
            <Input
              type="number"
              min={0}
              value={maxBudget}
              onChange={(e) =>
                setMaxBudget(Math.max(0, Number(e.target.value) || 0))
              }
              className="font-mono"
            />
          </Field>

          <Field label="Allowed model families">
            <div className="flex flex-wrap gap-3 text-xs">
              {MODEL_FAMILIES.map((f) => (
                <label
                  key={f}
                  className="inline-flex items-center gap-1.5 cursor-pointer"
                >
                  <input
                    type="checkbox"
                    checked={allowedFamilies.has(f)}
                    onChange={() => toggleSet(setAllowedFamilies)(f)}
                  />
                  <span className="font-mono">{f}</span>
                </label>
              ))}
            </div>
          </Field>

          <Field label="Required compliance">
            <div className="flex flex-wrap gap-3 text-xs">
              {COMPLIANCE_FLAGS.map((f) => (
                <label
                  key={f}
                  className="inline-flex items-center gap-1.5 cursor-pointer"
                >
                  <input
                    type="checkbox"
                    checked={requiredCompliance.has(f)}
                    onChange={() => toggleSet(setRequiredCompliance)(f)}
                  />
                  <span className="font-mono">{f}</span>
                </label>
              ))}
            </div>
          </Field>

          <Field label="Allowed override paths">
            <div className="flex flex-wrap gap-3 text-xs">
              {OVERRIDE_PATH_SUGGESTIONS.map((p) => (
                <label
                  key={p}
                  className="inline-flex items-center gap-1.5 cursor-pointer"
                >
                  <input
                    type="checkbox"
                    checked={allowedOverrides.has(p)}
                    onChange={() => toggleSet(setAllowedOverrides)(p)}
                  />
                  <span className="font-mono">{p}</span>
                </label>
              ))}
            </div>
          </Field>

          <Field
            label="Default values (JSON)"
            error={defaultsErr ?? null}
            hint="The baseline helm values for every tenant from this template."
          >
            <textarea
              value={defaultValuesYaml}
              onChange={(e) => setDefaultValuesYaml(e.target.value)}
              rows={8}
              className="w-full border border-border/60 rounded-md bg-background/40 px-3 py-2 text-xs font-mono focus:outline-none focus:border-primary/40"
              placeholder='{ "platform": { "tenant": "acme" }, "budget": { "monthlyUsd": 2500 } }'
            />
          </Field>

          {existing && <TemplateAccessSection templateID={existing.id} />}

          <div className="flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => mutation.mutate()}
              disabled={!canSubmit}
            >
              {mutation.isPending
                ? "Saving..."
                : existing
                ? "Save changes"
                : "Create Template"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function TemplateAccessSection({ templateID }: { templateID: string }) {
  const queryClient = useQueryClient();
  const [picker, setPicker] = useState("");

  const { data: access } = useQuery({
    queryKey: ["template", templateID, "access"],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/templates/{templateID}/access",
        { params: { path: { templateID } } },
      );
      if (error) throw error;
      return data!;
    },
  });

  const { data: teams } = useQuery({
    queryKey: ["teams", "all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/teams");
      if (error) throw error;
      return data!;
    },
  });

  const grant = useMutation({
    mutationFn: async (teamID: string) => {
      const { data, error } = await api.POST(
        "/templates/{templateID}/access",
        { params: { path: { templateID } }, body: { team_id: teamID } },
      );
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["template", templateID, "access"],
      });
      toast.success("Access granted");
      setPicker("");
    },
    onError: () => toast.error("Failed to grant access"),
  });

  const revoke = useMutation({
    mutationFn: async (teamID: string) => {
      const { error } = await api.DELETE(
        "/templates/{templateID}/access/{teamId}",
        { params: { path: { templateID, teamId: teamID } } },
      );
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["template", templateID, "access"],
      });
      toast.success("Access revoked");
    },
    onError: () => toast.error("Failed to revoke access"),
  });

  const grantedIDs = new Set((access ?? []).map((a: TemplateTeamAccess) => a.team_id));
  const ungranted = (teams ?? []).filter((t: Team) => !grantedIDs.has(t.id));
  const teamName = (id: string) =>
    (teams ?? []).find((t: Team) => t.id === id)?.name ?? id;

  return (
    <div className="border border-border/60 rounded-lg overflow-hidden">
      <div className="px-4 py-2 text-xs font-medium border-b border-border/40 bg-accent/20">
        Team Access ({access?.length ?? 0})
      </div>
      <div className="divide-y divide-border/30">
        {(access ?? []).length === 0 ? (
          <div className="px-4 py-2.5 text-[11px] text-muted-foreground">
            No teams can use this template yet. Admins always see it.
          </div>
        ) : (
          (access ?? []).map((a: TemplateTeamAccess) => (
            <div
              key={a.id}
              className="px-4 py-2 text-xs flex items-center gap-3"
            >
              <span className="font-medium flex-1">{teamName(a.team_id)}</span>
              <span className="text-[11px] text-muted-foreground/70">
                {formatRelativeTime(a.granted_at)}
              </span>
              <button
                onClick={() => {
                  if (confirm(`Revoke access from ${teamName(a.team_id)}?`)) {
                    revoke.mutate(a.team_id);
                  }
                }}
                className="text-muted-foreground hover:text-destructive transition-colors cursor-pointer"
                aria-label="Revoke"
              >
                <Trash2 className="w-3 h-3" />
              </button>
            </div>
          ))
        )}
        {ungranted.length > 0 && (
          <div className="px-4 py-2 bg-background/40 flex items-center gap-2">
            <select
              value={picker}
              onChange={(e) => setPicker(e.target.value)}
              className="text-xs border border-border/60 rounded-md bg-background px-2 py-1.5 flex-1"
            >
              <option value="">Grant access to a team…</option>
              {ungranted.map((t: Team) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
            <Button
              size="sm"
              onClick={() => picker && grant.mutate(picker)}
              disabled={!picker || grant.isPending}
            >
              Grant
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
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
