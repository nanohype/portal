import { useState, useEffect } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Template } from "@/api/types";
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

// Personas, model families, and compliance flags come from the EAP chart's
// known taxonomies. Keeping these hardcoded here trades chart-evolution
// freshness for form responsiveness — when EAP adds an option, bump it here
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
// operators can change. Matches the EAP `charts/tenant` values schema.
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
