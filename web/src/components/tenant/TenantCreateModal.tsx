import { useState, useEffect, useId } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { useAuth } from '@/hooks/useAuth';
import type { Cluster, Template } from '@/api/models';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog';
import { buildOverrides } from './build-overrides';
import { DatastoreFields } from './DatastoreFields';
import {
  datastoreError,
  fromDatastores,
  toDatastores,
  type DatastoreDraft,
} from '@/lib/datastores';

// Maps a form-level state var to the dotted helm-values path it controls.
// The handler uses this allowlist to decide whether a field is editable for
// a template-driven submission: a field whose path isn't in
// template.allowed_overrides is disabled and inherits the template default.
//
// These strings have to match the tenant chart's values keys exactly, and the
// same set is offered as suggestions in the template editor — a path spelled
// differently in either place is a dead allowlist entry.
const FIELD_PATHS = {
  monthlyUsd: 'budget.monthlyUsd',
  persona: 'platform.persona',
  displayName: 'platform.displayName',
  tenant: 'platform.tenant',
  soc2: 'platform.compliance.soc2',
  hipaa: 'platform.compliance.hipaa',
  datastores: 'datastores',
  capabilities: 'identity.capabilities',
  directSecretReads: 'identity.directSecretReads',
  attributionOperators: 'attribution.operators',
} as const;

// The managed AWS capabilities outside the datastore vocabulary. Mirrored from
// the Platform CRD's Capability enum.
const CAPABILITIES = [
  {
    value: 'ses',
    label: 'ses',
    hint: 'Send mail. Scoped by a FromAddress condition derived from the platform name.',
  },
  {
    value: 'eventBridgeScheduler',
    label: 'eventBridgeScheduler',
    hint: "Manage schedules that deliver into this tenant's own queues. Needs a queue datastore.",
  },
] as const;

// Splits a comma/newline separated textarea into a clean list.
function parseList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter((s) => s !== '');
}

function asStringList(raw: unknown): string[] {
  return Array.isArray(raw) ? raw.filter((v): v is string => typeof v === 'string') : [];
}

const K8S_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

// Personas mirrored from charts/tenant/values.yaml. Keeping the list here
// (rather than fetching from the chart at runtime) trades chart-change
// freshness for form responsiveness; if eks-agent-platform adds a persona we update both
// the chart and this list together.
const PERSONAS = [
  'generic',
  'sales-ops',
  'support',
  'finance',
  'ops',
  'founder',
  'eng',
  'marketing',
  'legal',
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
  const uid = useId();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'owner';

  const { data: templates } = useQuery({
    queryKey: ['templates'],
    queryFn: async () => {
      const { data, error } = await api.GET('/templates');
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: open,
  });

  // Teams the user can pick as the owning team for the new tenant. Admins
  // see all org teams (optional pick = no ownership = admin-only visibility);
  // non-admins see only their own teams (server rejects bad picks anyway).
  const { data: pickableTeams } = useQuery({
    queryKey: ['teams', isAdmin ? 'all' : 'mine'],
    queryFn: async () => {
      const { data, error } = await api.GET('/teams', {
        params: isAdmin ? {} : { query: { member_of: 'me' } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: open,
  });

  // Mode: "template" (operators default) vs "scratch" (admin advanced).
  // When templates exist + user picks one, fields outside its
  // allowed_overrides are disabled and inherit template defaults.
  const [templateID, setTemplateID] = useState<string>('');
  const [scratchMode, setScratchMode] = useState(false);
  const selected = templates?.find((t) => t.id === templateID);

  const [owningTeamID, setOwningTeamID] = useState('');
  const [clusterID, setClusterID] = useState('');
  const [name, setName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [tenant, setTenant] = useState('');
  const [persona, setPersona] = useState<(typeof PERSONAS)[number]>('generic');
  const [monthlyUsd, setMonthlyUsd] = useState(500);
  const [hipaa, setHipaa] = useState(false);
  const [soc2, setSoc2] = useState(true);
  const [datastores, setDatastores] = useState<DatastoreDraft[]>([]);
  const [capabilities, setCapabilities] = useState<string[]>([]);
  const [secretReads, setSecretReads] = useState('');
  const [attributionOperators, setAttributionOperators] = useState('');

  const reset = () => {
    setTemplateID('');
    setScratchMode(false);
    setOwningTeamID('');
    setClusterID('');
    setName('');
    setDisplayName('');
    setTenant('');
    setPersona('generic');
    setMonthlyUsd(500);
    setHipaa(false);
    setSoc2(true);
    setDatastores([]);
    setCapabilities([]);
    setSecretReads('');
    setAttributionOperators('');
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
    if (typeof platform.persona === 'string') {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional prefill of editable fields from the picked template's defaults
      setPersona(platform.persona as (typeof PERSONAS)[number]);
    } else {
      setPersona(selected.persona as (typeof PERSONAS)[number]);
    }
    if (typeof platform.displayName === 'string') setDisplayName(platform.displayName);
    if (typeof platform.tenant === 'string') setTenant(platform.tenant);
    if (typeof budget.monthlyUsd === 'number') setMonthlyUsd(budget.monthlyUsd);
    if (typeof compliance.soc2 === 'boolean') setSoc2(compliance.soc2);
    if (typeof compliance.hipaa === 'boolean') setHipaa(compliance.hipaa);

    // The vocabulary prefills the same way, so the form shows what the tenant
    // will actually declare rather than an empty editor next to a template that
    // already carries a substrate.
    const identity = (d?.identity ?? {}) as Record<string, unknown>;
    const attribution = (d?.attribution ?? {}) as Record<string, unknown>;
    setDatastores(fromDatastores(d?.datastores));
    setCapabilities(asStringList(identity.capabilities));
    setSecretReads(asStringList(identity.directSecretReads).join('\n'));
    setAttributionOperators(asStringList(attribution.operators).join('\n'));
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
      // Template mode: the template's allowed_overrides is the allowlist (the
      // same set `allowed()` gates the fields on). buildOverrides drops locked
      // paths and any prototype-polluting segment.
      const overrides = buildOverrides(selected.allowed_overrides, [
        [FIELD_PATHS.monthlyUsd, monthlyUsd],
        [FIELD_PATHS.persona, persona],
        [FIELD_PATHS.displayName, displayName || name],
        [FIELD_PATHS.tenant, tenant || name],
        [FIELD_PATHS.soc2, soc2],
        [FIELD_PATHS.hipaa, hipaa],
        [FIELD_PATHS.datastores, toDatastores(datastores)],
        [FIELD_PATHS.capabilities, capabilities],
        [FIELD_PATHS.directSecretReads, parseList(secretReads)],
        [FIELD_PATHS.attributionOperators, parseList(attributionOperators)],
      ]);
      return {
        cluster_id: clusterID,
        name,
        template_id: selected.id,
        owning_team_id: owningTeamID || undefined,
        values: overrides,
      };
    }
    // Scratch mode sends the full blob. The vocabulary keys are omitted when
    // empty rather than sent as empty lists, so a plain tenant renders exactly
    // the chart's defaults — and attribution in particular is rejected by the
    // CRD if the block is present with no operators.
    const operators = parseList(attributionOperators);
    const reads = parseList(secretReads);
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
        ...(capabilities.length > 0 || reads.length > 0
          ? {
              identity: {
                ...(capabilities.length > 0 ? { capabilities } : {}),
                ...(reads.length > 0 ? { directSecretReads: reads } : {}),
              },
            }
          : {}),
        ...(operators.length > 0 ? { attribution: { operators } } : {}),
        ...(datastores.length > 0 ? { datastores: toDatastores(datastores) } : {}),
      },
    };
  };

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/tenants', { body: buildBody() });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tenants'] });
      toast.success('Tenant create enqueued · ArgoCD will reconcile shortly');
      reset();
      onClose();
    },
    onError: (e: unknown) => {
      const msg = (e as { message?: string })?.message ?? 'Failed to enqueue tenant';
      toast.error(msg);
    },
  });

  const nameInvalid = name !== '' && !K8S_NAME_RE.test(name);
  const budgetOverCap =
    selected && selected.max_budget_usd > 0 && monthlyUsd > selected.max_budget_usd;
  const needsTeamPick =
    !isAdmin && pickableTeams !== undefined && pickableTeams.length > 1 && owningTeamID === '';
  const noTeams = !isAdmin && pickableTeams !== undefined && pickableTeams.length === 0;
  // The composed-name budget depends on the tenant name, so a legal row can turn
  // illegal as the name is typed. Re-checked here rather than only per row.
  const datastoresInvalid = datastores.some((d) => datastoreError(d, name, datastores) !== null);
  // The operator scopes the minted scheduler-invoke role's SendMessage to the
  // tenant's own queues, so the capability with no queue is a role carrying no
  // grant. The chart rejects it at render; catching it here keeps the operator
  // out of a failed git job.
  const schedulerWithoutQueue =
    capabilities.includes('eventBridgeScheduler') && !datastores.some((d) => d.kind === 'queue');
  const canSubmit =
    clusterID !== '' &&
    K8S_NAME_RE.test(name) &&
    monthlyUsd > 0 &&
    !budgetOverCap &&
    !datastoresInvalid &&
    !schedulerWithoutQueue &&
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
            Renders the eks-agent-platform `charts/tenant` chart with these values and commits to
            the tenants repo. ArgoCD reconciles the result onto the chosen cluster.
          </DialogDescription>
        </DialogHeader>

        <div className="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
          {templates && templates.length > 0 && (
            <Field label="Template" htmlFor={`${uid}-template`} className="sm:col-span-2">
              <div className="flex items-center gap-2">
                <Select
                  id={`${uid}-template`}
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
                      {t.max_budget_usd > 0 ? ` · ≤ $${t.max_budget_usd}/mo` : ''}
                    </option>
                  ))}
                </Select>
                {isAdmin && (
                  <button
                    type="button"
                    onClick={() => {
                      setScratchMode((s) => !s);
                      setTemplateID('');
                    }}
                    className="text-[11px] text-muted-foreground hover:text-foreground transition-colors cursor-pointer whitespace-nowrap"
                  >
                    {scratchMode ? 'use template' : 'from scratch'}
                  </button>
                )}
              </div>
              {selected && (
                <p className="text-[11px] text-muted-foreground/70 mt-1">
                  {selected.description || 'Operator overrides limited to: '}
                  <span className="font-mono">
                    {selected.allowed_overrides.join(', ') || 'none'}
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
              You must belong to a team before creating tenants. Ask an admin to add you to one.
            </div>
          )}

          {pickableTeams && pickableTeams.length > 0 && (
            <Field
              label={isAdmin ? 'Owning team (optional)' : 'Owning team'}
              htmlFor={`${uid}-owning-team`}
              hint={
                isAdmin
                  ? 'Leave blank for admin-only visibility.'
                  : pickableTeams.length === 1
                    ? `Auto-assigned to your team: ${pickableTeams[0].name}`
                    : undefined
              }
            >
              <Select
                id={`${uid}-owning-team`}
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

          <Field label="Cluster" htmlFor={`${uid}-cluster`}>
            <Select
              id={`${uid}-cluster`}
              value={clusterID}
              onChange={(e) => setClusterID(e.target.value)}
            >
              <option value="">Pick a cluster…</option>
              {clusters
                .filter((c) => c.connection_status === 'connected')
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} ({c.region})
                  </option>
                ))}
            </Select>
            {clusters.filter((c) => c.connection_status === 'connected').length === 0 && (
              <p className="text-[11px] text-warning mt-1">
                No clusters are currently connected. Register one first.
              </p>
            )}
          </Field>

          <Field
            label="Platform name (k8s name)"
            htmlFor={`${uid}-name`}
            error={
              nameInvalid
                ? 'Lowercase alphanumeric + hyphen, 1-63 chars, must start/end alphanumeric'
                : null
            }
          >
            <Input
              id={`${uid}-name`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="marketing-team"
              className="font-mono"
            />
          </Field>

          <Field
            label="Display name (optional)"
            htmlFor={`${uid}-display-name`}
            locked={!allowed(FIELD_PATHS.displayName)}
          >
            <Input
              id={`${uid}-display-name`}
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Marketing Team"
              disabled={!allowed(FIELD_PATHS.displayName)}
            />
          </Field>

          <Field
            label="Parent Tenant (defaults to platform name)"
            htmlFor={`${uid}-tenant`}
            locked={!allowed(FIELD_PATHS.tenant)}
          >
            <Input
              id={`${uid}-tenant`}
              value={tenant}
              onChange={(e) => setTenant(e.target.value)}
              placeholder="leave blank to use platform name"
              className="font-mono"
              disabled={!allowed(FIELD_PATHS.tenant)}
            />
          </Field>

          <Field label="Persona" htmlFor={`${uid}-persona`} locked={!allowed(FIELD_PATHS.persona)}>
            <Select
              id={`${uid}-persona`}
              value={persona}
              onChange={(e) => setPersona(e.target.value as (typeof PERSONAS)[number])}
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
            htmlFor={`${uid}-monthly-usd`}
            locked={!allowed(FIELD_PATHS.monthlyUsd)}
            error={budgetOverCap ? `Exceeds template cap of $${selected!.max_budget_usd}` : null}
            hint={
              selected && selected.max_budget_usd > 0
                ? `Template cap: $${selected.max_budget_usd.toLocaleString()}/mo`
                : undefined
            }
          >
            <Input
              id={`${uid}-monthly-usd`}
              type="number"
              min={1}
              value={monthlyUsd}
              onChange={(e) => setMonthlyUsd(Math.max(0, Number(e.target.value) || 0))}
              className="font-mono"
              disabled={!allowed(FIELD_PATHS.monthlyUsd)}
            />
          </Field>

          <Field label="Compliance">
            <div className="flex items-center gap-4 text-xs">
              <label
                htmlFor={`${uid}-soc2`}
                className="inline-flex items-center gap-2 cursor-pointer"
              >
                <input
                  id={`${uid}-soc2`}
                  type="checkbox"
                  checked={soc2}
                  onChange={(e) => setSoc2(e.target.checked)}
                  disabled={!allowed(FIELD_PATHS.soc2)}
                />
                SOC 2
                {selected?.required_compliance.includes('soc2') && (
                  <Badge variant="warning">required</Badge>
                )}
              </label>
              <label
                htmlFor={`${uid}-hipaa`}
                className="inline-flex items-center gap-2 cursor-pointer"
              >
                <input
                  id={`${uid}-hipaa`}
                  type="checkbox"
                  checked={hipaa}
                  onChange={(e) => setHipaa(e.target.checked)}
                  disabled={!allowed(FIELD_PATHS.hipaa)}
                />
                HIPAA
                {selected?.required_compliance.includes('hipaa') && (
                  <Badge variant="warning">required</Badge>
                )}
              </label>
            </div>
          </Field>

          <Field
            label="Datastores"
            className="sm:col-span-2"
            locked={!allowed(FIELD_PATHS.datastores)}
            hint="Declaring a datastore grants this tenant access to it and reports it on the Platform. The resource is provisioned when the declaration reaches the landing-zone tenant-substrate input."
          >
            <DatastoreFields
              value={datastores}
              onChange={setDatastores}
              platformName={name}
              allowedKinds={
                selected && !scratchMode ? (selected.allowed_datastore_kinds ?? []) : []
              }
              disabled={!allowed(FIELD_PATHS.datastores)}
            />
          </Field>

          <Field
            label="Capabilities"
            locked={!allowed(FIELD_PATHS.capabilities)}
            error={
              schedulerWithoutQueue
                ? 'eventBridgeScheduler needs a queue datastore to send to — without one the minted role carries no grant'
                : null
            }
          >
            <div className="flex flex-col gap-1.5">
              {CAPABILITIES.map((c) => (
                <label
                  key={c.value}
                  htmlFor={`${uid}-cap-${c.value}`}
                  className="inline-flex items-start gap-2 text-xs cursor-pointer"
                >
                  <input
                    id={`${uid}-cap-${c.value}`}
                    type="checkbox"
                    className="mt-0.5"
                    checked={capabilities.includes(c.value)}
                    onChange={(e) =>
                      setCapabilities(
                        e.target.checked
                          ? [...capabilities, c.value]
                          : capabilities.filter((v) => v !== c.value),
                      )
                    }
                    disabled={!allowed(FIELD_PATHS.capabilities)}
                  />
                  <span>
                    <span className="font-mono">{c.label}</span>
                    <span className="block text-[11px] text-muted-foreground/70">{c.hint}</span>
                  </span>
                </label>
              ))}
            </div>
          </Field>

          <Field
            label="Direct secret reads"
            htmlFor={`${uid}-secret-reads`}
            locked={!allowed(FIELD_PATHS.directSecretReads)}
            hint="One per line, relative to <platform>/<environment>/ — write oncall/webhook-hmac, not the full path."
          >
            <textarea
              id={`${uid}-secret-reads`}
              value={secretReads}
              onChange={(e) => setSecretReads(e.target.value)}
              rows={3}
              placeholder={'oncall/webhook-hmac\nvendor-api-token'}
              disabled={!allowed(FIELD_PATHS.directSecretReads)}
              className="w-full rounded-md border border-border/60 bg-transparent px-2.5 py-1.5 text-xs font-mono disabled:opacity-40"
            />
          </Field>

          <Field
            label="Attribution operators"
            htmlFor={`${uid}-attribution`}
            className="sm:col-span-2"
            locked={!allowed(FIELD_PATHS.attributionOperators)}
            hint="One lowercased identity per line. Naming anyone turns attribution on: sessions then carry that person as STS SourceIdentity and may impersonate them at the apiserver, so the string must match their Kubernetes RBAC subject exactly. Leave empty for an unattributed tenant."
          >
            <textarea
              id={`${uid}-attribution`}
              value={attributionOperators}
              onChange={(e) => setAttributionOperators(e.target.value)}
              rows={2}
              placeholder="operator@example.com"
              disabled={!allowed(FIELD_PATHS.attributionOperators)}
              className="w-full rounded-md border border-border/60 bg-transparent px-2.5 py-1.5 text-xs font-mono disabled:opacity-40"
            />
          </Field>

          <div className="sm:col-span-2 flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button size="sm" onClick={() => mutation.mutate()} disabled={!canSubmit}>
              {mutation.isPending ? 'Enqueueing...' : 'Create Tenant'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  error,
  locked,
  className,
  children,
}: {
  label: string;
  htmlFor?: string;
  hint?: string;
  error?: string | null;
  locked?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={className}>
      <label
        htmlFor={htmlFor}
        className="text-xs font-medium text-muted-foreground mb-1.5 flex items-center gap-1.5"
      >
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
