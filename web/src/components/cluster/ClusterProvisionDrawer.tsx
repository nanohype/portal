import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { useId, useState } from 'react';
import { toast } from 'sonner';
import { api } from '@/api/client';
import type { Account, ClusterOperation } from '@/api/models';
import { Button } from '@/components/ui/button';
import { ChipToggle } from '@/components/ui/chip-toggle';
import {
  Drawer,
  DrawerBody,
  DrawerDescription,
  DrawerFooter,
  DrawerHeader,
  DrawerTitle,
} from '@/components/ui/drawer';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { navigate } from '@/hooks/useNavigate';
import { CIDR_RE } from '@/lib/cidr';
import {
  buildNetwork,
  buildSystemNodes,
  buildTtlDays,
  emptyNetworkForm,
  emptySystemNodesForm,
  FLEET_DEFAULTS,
  type NetworkForm,
  networkErrors,
  networkSummary,
  type SystemNodesForm,
  systemNodesErrors,
  systemNodesSummary,
  ttlDaysError,
  ttlSummary,
} from '@/lib/cluster-order';
import { parseCommaList } from '@/lib/list';
import { cn } from '@/lib/utils';
import { VendTimeline } from './VendTimeline';

const AWS_REGION_RE = /^[a-z]{2}-[a-z]+-\d$/;
const K8S_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

// The vend "order desk" as a slide-over: it produces an eks-fleet Cluster CR
// (committed to the clusters GitOps repo) rather than registering an existing
// cluster. On submit the drawer morphs from the form into a live provisioning
// view for the just-placed order — you watch it advance in place instead of
// hunting the page for it. The provision watch-back auto-registers the cluster
// once it comes up, so there's no credential entry here.
export function ClusterProvisionDrawer({
  open,
  onClose,
  accounts,
}: {
  open: boolean;
  onClose: () => void;
  accounts: Account[];
}) {
  const queryClient = useQueryClient();
  const uid = useId();
  const [accountID, setAccountID] = useState('');
  const [name, setName] = useState('');
  const [team, setTeam] = useState('');
  const [environment, setEnvironment] = useState<'development' | 'staging' | 'production'>(
    'development',
  );
  const [region, setRegion] = useState('');
  const [clusterVersion, setClusterVersion] = useState('');
  // Private-by-default: the fleet's Cluster XRD defaults endpointPublicAccess
  // to false, and its CEL rule rejects a public opt-in without a CIDR
  // allowlist. The form mirrors both so a bad order can't be placed.
  const [publicAccess, setPublicAccess] = useState(false);
  const [publicCidrs, setPublicCidrs] = useState('');
  // Born light: floor is the XRD default, and full has a substrate prerequisite
  // (managed monitoring must already exist for this cluster), so opting up is a
  // deliberate act rather than the path of least resistance.
  const [observabilityTier, setObservabilityTier] = useState<'floor' | 'full'>('floor');
  // The three blocks that carry the fleet defaults when untouched. Each is
  // collapsed to a one-line summary of what it will actually produce, so an
  // unopened section is a stated choice rather than an unknown. See
  // lib/cluster-order for the builders and the refusals they mirror.
  const [network, setNetwork] = useState<NetworkForm>(emptyNetworkForm);
  const [systemNodes, setSystemNodes] = useState<SystemNodesForm>(emptySystemNodesForm);
  const [ttlDays, setTtlDays] = useState('');
  const [showNetwork, setShowNetwork] = useState(false);
  const [showNodes, setShowNodes] = useState(false);
  // Set on a successful order → the drawer switches to the live timeline view.
  const [orderedId, setOrderedId] = useState<string | null>(null);

  const patchNetwork = (patch: Partial<NetworkForm>) => setNetwork((n) => ({ ...n, ...patch }));
  const patchNodes = (patch: Partial<SystemNodesForm>) =>
    setSystemNodes((n) => ({ ...n, ...patch }));

  const selectedAccount = accounts.find((a) => a.id === accountID);

  const reset = () => {
    setAccountID('');
    setName('');
    setTeam('');
    setEnvironment('development');
    setRegion('');
    setClusterVersion('');
    setPublicAccess(false);
    setPublicCidrs('');
    setObservabilityTier('floor');
    setNetwork(emptyNetworkForm);
    setSystemNodes(emptySystemNodesForm);
    setTtlDays('');
    setShowNetwork(false);
    setShowNodes(false);
    setOrderedId(null);
  };

  // Picking an account pre-fills the region with its default — most vends land
  // in the account's home region; the operator can still override.
  const onPickAccount = (id: string) => {
    setAccountID(id);
    const acct = accounts.find((a) => a.id === id);
    if (acct && region === '') setRegion(acct.default_region);
  };

  const orderMutation = useMutation({
    mutationFn: async () => {
      if (!selectedAccount) throw new Error('Pick an account');
      const { data, error } = await api.POST('/cluster-orders', {
        body: {
          name: name.trim(),
          account: selectedAccount.aws_account_id,
          region: region.trim(),
          team: team.trim(),
          environment,
          cluster_version: clusterVersion.trim() || undefined,
          endpoint_public_access: publicAccess,
          endpoint_public_access_cidrs: publicAccess ? parseCommaList(publicCidrs) : undefined,
          observability_tier: observabilityTier,
          network: buildNetwork(network),
          system_nodes: buildSystemNodes(systemNodes),
          ttl_days: buildTtlDays(ttlDays),
        },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: (op) => {
      // The order renders from the ['cluster-operations'] surface — seed the new
      // op into that cache so it appears instantly rather than on the next poll
      // tick, then invalidate the order surfaces to reconcile with the server.
      queryClient.setQueryData<ClusterOperation[]>(['cluster-operations'], (prev) => [
        op,
        ...(prev ?? []).filter((o) => o.id !== op.id),
      ]);
      queryClient.invalidateQueries({ queryKey: ['cluster-operations'] });
      queryClient.invalidateQueries({ queryKey: ['ops-feed'] });
      queryClient.invalidateQueries({ queryKey: ['clusters'] });
      toast.success(`Provisioning ${name.trim()} · self-registers when ready`, {
        action: { label: 'Ops', onClick: () => navigate('/ops') },
      });
      setOrderedId(op.id); // morph to the live view — don't close
    },
    onError: (e: unknown) => {
      toast.error((e as { message?: string })?.message ?? 'Failed to order cluster');
    },
  });

  // Live op for the result view. Shares the ['cluster-operations'] cache, but
  // polls it itself while an order is in flight so the timeline advances
  // queued → committed → building → active no matter which page opened the
  // drawer (don't lean on ClusterList being mounted). Self-limiting: stops once
  // the op reaches a terminal or a portal-side failure.
  const { data: ops } = useQuery({
    queryKey: ['cluster-operations'],
    queryFn: async () => {
      const { data, error } = await api.GET('/cluster-orders');
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: orderedId !== null,
    refetchInterval: (query) => {
      if (orderedId === null) return false;
      const op = query.state.data?.find((o) => o.id === orderedId);
      if (!op) return 3000; // just placed, not in cache yet — keep checking
      if (op.status === 'active' || op.status === 'failed' || op.status === 'deprovisioned')
        return false;
      if ('failed' in (op.vend_phases ?? {})) return false;
      return 3000;
    },
  });
  const liveOp = ops?.find((o) => o.id === orderedId) ?? null;

  const handleClose = () => {
    reset();
    onClose();
  };

  const regionInvalid = region !== '' && !AWS_REGION_RE.test(region);
  const teamInvalid = team !== '' && !K8S_NAME_RE.test(team);
  const nameInvalid = name !== '' && !K8S_NAME_RE.test(name);
  // Opting into a public endpoint makes the CIDR allowlist required — the
  // fleet's CEL rule rejects public-without-allowlist, so gate submit here.
  const cidrList = parseCommaList(publicCidrs);
  const cidrsMissing = publicAccess && cidrList.length === 0;
  const cidrsMalformed = publicAccess && cidrList.some((c) => !CIDR_RE.test(c));
  const cidrError = cidrsMissing
    ? 'Required to enable the public endpoint — comma-separated CIDRs, e.g. 203.0.113.0/24'
    : cidrsMalformed
      ? 'Each entry must be a CIDR like 203.0.113.0/24'
      : null;
  // Every rule in these three is also enforced by clusterspec.Validate, so an
  // order that fails them is a 400 the operator has no path around — the point of
  // checking here is that the field is named while they are still looking at it.
  const netErrors = networkErrors(network);
  const nodeErrors = systemNodesErrors(systemNodes);
  const ttlError = ttlDaysError(ttlDays);
  const canSubmit =
    accountID !== '' &&
    name.trim() !== '' &&
    !nameInvalid &&
    team.trim() !== '' &&
    !teamInvalid &&
    AWS_REGION_RE.test(region) &&
    !cidrsMissing &&
    !cidrsMalformed &&
    Object.keys(netErrors).length === 0 &&
    Object.keys(nodeErrors).length === 0 &&
    ttlError === undefined;

  const ordered = orderedId !== null;

  return (
    <Drawer open={open} onClose={handleClose}>
      <DrawerHeader onClose={handleClose}>
        <DrawerTitle>{ordered ? `Provisioning ${name.trim()}` : 'Provision Cluster'}</DrawerTitle>
        <DrawerDescription>
          {ordered
            ? 'Portal committed the Cluster definition. Crossplane vends it; it registers here once its API is up.'
            : 'Order a new EKS cluster. Portal commits a Cluster definition to the fleet; Crossplane vends it.'}
        </DrawerDescription>
      </DrawerHeader>

      <DrawerBody>
        {ordered ? (
          <div className="animate-fade-in space-y-6">
            {liveOp ? (
              <VendTimeline op={liveOp} />
            ) : (
              <div className="text-xs text-muted-foreground">Committing the order…</div>
            )}
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-[12px]">
              <Detail label="Account" value={selectedAccount?.name ?? '—'} />
              <Detail label="Region" value={region} mono />
              <Detail label="Environment" value={environment} />
              <Detail label="Team" value={team.trim()} mono />
              <Detail label="Network" value={networkSummary(network)} />
              <Detail label="Node group" value={systemNodesSummary(systemNodes)} />
              <Detail label="Lifetime" value={ttlSummary(ttlDays)} />
            </dl>
            <p className="text-[12px] leading-relaxed text-muted-foreground">
              The full journey — commit → build → active — streams in{' '}
              <span className="text-foreground">Ops</span>. You can close this and it keeps going.
            </p>
          </div>
        ) : (
          <div className="space-y-4">
            <Field label="Account" htmlFor={`${uid}-account`}>
              <Select
                id={`${uid}-account`}
                value={accountID}
                onChange={(e) => onPickAccount(e.target.value)}
              >
                <option value="">Pick an account…</option>
                {accounts.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name} ({a.aws_account_id})
                  </option>
                ))}
              </Select>
            </Field>

            <Field
              label="Name"
              htmlFor={`${uid}-name`}
              error={nameInvalid ? 'Lowercase letters, digits, and dashes' : null}
            >
              <Input
                id={`${uid}-name`}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="platform"
                className="font-mono"
              />
            </Field>

            <Field
              label="Team"
              htmlFor={`${uid}-team`}
              error={teamInvalid ? 'Lowercase letters, digits, and dashes (k8s namespace)' : null}
            >
              <Input
                id={`${uid}-team`}
                value={team}
                onChange={(e) => setTeam(e.target.value)}
                placeholder="apps"
                className="font-mono"
              />
            </Field>

            <Field label="Environment" htmlFor={`${uid}-environment`}>
              <Select
                id={`${uid}-environment`}
                value={environment}
                onChange={(e) =>
                  setEnvironment(e.target.value as 'development' | 'staging' | 'production')
                }
              >
                <option value="development">development</option>
                <option value="staging">staging</option>
                <option value="production">production</option>
              </Select>
            </Field>

            <Field
              label="Region"
              htmlFor={`${uid}-region`}
              error={regionInvalid ? 'Must look like us-west-2' : null}
            >
              <Input
                id={`${uid}-region`}
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                placeholder="us-west-2"
                className="font-mono"
              />
            </Field>

            <Field label="Kubernetes Version (optional)" htmlFor={`${uid}-cluster-version`}>
              <Input
                id={`${uid}-cluster-version`}
                value={clusterVersion}
                onChange={(e) => setClusterVersion(e.target.value)}
                placeholder="Defaults to the fleet default (e.g. 1.36)"
                className="font-mono"
              />
            </Field>

            <Field label="Observability" htmlFor={`${uid}-observability-tier`}>
              <Select
                id={`${uid}-observability-tier`}
                value={observabilityTier}
                onChange={(e) => setObservabilityTier(e.target.value as 'floor' | 'full')}
              >
                <option value="floor">floor — CloudWatch</option>
                <option value="full">full — CloudWatch + LGTM + managed Prometheus/Grafana</option>
              </Select>
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                Tenant workloads are identical either way — both tiers run the same OpenTelemetry
                agent and gateway, and only the destinations differ. full needs the
                managed-monitoring substrate to already exist for this cluster.
              </p>
            </Field>

            <div className="space-y-2">
              <label
                htmlFor={`${uid}-public-access`}
                className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer"
              >
                <input
                  id={`${uid}-public-access`}
                  type="checkbox"
                  checked={publicAccess}
                  onChange={(e) => setPublicAccess(e.target.checked)}
                  className="accent-primary"
                />
                Public API endpoint
              </label>
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                Clusters vend with a private API endpoint. Turning this on exposes it publicly and
                requires a CIDR allowlist scoping who can reach it.
              </p>
            </div>

            {publicAccess && (
              <Field label="Allowed CIDRs" htmlFor={`${uid}-public-cidrs`} error={cidrError}>
                <Input
                  id={`${uid}-public-cidrs`}
                  value={publicCidrs}
                  onChange={(e) => setPublicCidrs(e.target.value)}
                  placeholder="203.0.113.0/24, 198.51.100.7/32"
                  className="font-mono"
                />
              </Field>
            )}

            <Disclosure
              label="Networking"
              summary={networkSummary(network)}
              open={showNetwork}
              onToggle={() => setShowNetwork(!showNetwork)}
            >
              <div className="flex gap-2">
                <ChipToggle
                  active={network.mode === 'create'}
                  onClick={() => patchNetwork({ mode: 'create' })}
                >
                  Create a VPC
                </ChipToggle>
                <ChipToggle
                  active={network.mode === 'adopt'}
                  onClick={() => patchNetwork({ mode: 'adopt' })}
                >
                  Adopt a VPC
                </ChipToggle>
              </div>
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                {network.mode === 'create'
                  ? 'The stack owns the VPC and disposes of it with the cluster.'
                  : 'The cluster joins a VPC someone else provisioned — a shared VPC in this account, or one shared in over AWS RAM. Its owner runs the subnets and their tagging; the cluster only needs the ids.'}
              </p>

              {network.mode === 'create' ? (
                <>
                  <Field
                    label="VPC CIDR"
                    htmlFor={`${uid}-vpc-cidr`}
                    error={netErrors.vpcCidr}
                    hint={`Defaults to ${FLEET_DEFAULTS.vpcCidr}`}
                  >
                    <Input
                      id={`${uid}-vpc-cidr`}
                      value={network.vpcCidr}
                      onChange={(e) => patchNetwork({ vpcCidr: e.target.value })}
                      placeholder={FLEET_DEFAULTS.vpcCidr}
                      className="font-mono"
                    />
                  </Field>

                  <div className="grid grid-cols-2 gap-3">
                    <Field
                      label="IPAM Pool"
                      htmlFor={`${uid}-ipam-pool`}
                      error={netErrors.ipamPoolId}
                      hint="Draws the CIDR from a pool instead"
                    >
                      <Input
                        id={`${uid}-ipam-pool`}
                        value={network.ipamPoolId}
                        onChange={(e) => patchNetwork({ ipamPoolId: e.target.value })}
                        placeholder="ipam-pool-0a1b2c3d"
                        className="font-mono"
                      />
                    </Field>
                    <Field
                      label="IPAM Netmask"
                      htmlFor={`${uid}-ipam-netmask`}
                      error={netErrors.ipamNetmaskLength}
                      hint="16–20"
                    >
                      <Input
                        id={`${uid}-ipam-netmask`}
                        value={network.ipamNetmaskLength}
                        onChange={(e) => patchNetwork({ ipamNetmaskLength: e.target.value })}
                        placeholder="18"
                        className="font-mono"
                      />
                    </Field>
                  </div>

                  <Field
                    label="Transit Gateway"
                    htmlFor={`${uid}-tgw`}
                    error={netErrors.transitGatewayId}
                    hint="Attaches the VPC to a gateway"
                  >
                    <Input
                      id={`${uid}-tgw`}
                      value={network.transitGatewayId}
                      onChange={(e) => patchNetwork({ transitGatewayId: e.target.value })}
                      placeholder="tgw-0a1b2c3d"
                      className="font-mono"
                    />
                  </Field>

                  <label
                    htmlFor={`${uid}-centralized-egress`}
                    className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer"
                  >
                    <input
                      id={`${uid}-centralized-egress`}
                      type="checkbox"
                      checked={network.centralizedEgress}
                      onChange={(e) => patchNetwork({ centralizedEgress: e.target.checked })}
                      className="accent-primary"
                    />
                    Centralized egress
                  </label>
                  <p className="text-[11px] leading-relaxed text-muted-foreground">
                    Private egress routes through the transit gateway instead of a NAT gateway in
                    this VPC — cheaper per cluster, and inspected wherever the gateway sends it.
                  </p>

                  <div className="grid grid-cols-2 gap-3">
                    <Field
                      label="Max AZs"
                      htmlFor={`${uid}-max-azs`}
                      error={netErrors.maxAzs}
                      hint={`Defaults to ${FLEET_DEFAULTS.maxAzs}`}
                    >
                      <Input
                        id={`${uid}-max-azs`}
                        value={network.maxAzs}
                        onChange={(e) => patchNetwork({ maxAzs: e.target.value })}
                        placeholder={String(FLEET_DEFAULTS.maxAzs)}
                        className="font-mono"
                      />
                    </Field>
                    <Field
                      label="NAT Gateways"
                      htmlFor={`${uid}-nat-gateways`}
                      error={netErrors.natGateways}
                      hint={`Defaults to ${FLEET_DEFAULTS.natGateways}`}
                    >
                      <Input
                        id={`${uid}-nat-gateways`}
                        value={network.natGateways}
                        onChange={(e) => patchNetwork({ natGateways: e.target.value })}
                        placeholder={String(FLEET_DEFAULTS.natGateways)}
                        className="font-mono"
                      />
                    </Field>
                  </div>
                </>
              ) : (
                <>
                  <Field label="VPC" htmlFor={`${uid}-vpc-id`} error={netErrors.vpcId}>
                    <Input
                      id={`${uid}-vpc-id`}
                      value={network.vpcId}
                      onChange={(e) => patchNetwork({ vpcId: e.target.value })}
                      placeholder="vpc-0a1b2c3d"
                      className="font-mono"
                    />
                  </Field>
                  <Field
                    label="Private Subnets"
                    htmlFor={`${uid}-private-subnets`}
                    error={netErrors.privateSubnets}
                    hint="Comma-separated. Nodes land in these."
                  >
                    <Input
                      id={`${uid}-private-subnets`}
                      value={network.privateSubnets}
                      onChange={(e) => patchNetwork({ privateSubnets: e.target.value })}
                      placeholder="subnet-0a1b2c3d, subnet-0e4f5a6b"
                      className="font-mono"
                    />
                  </Field>
                  <Field
                    label="Public Subnets"
                    htmlFor={`${uid}-public-subnets`}
                    error={netErrors.publicSubnets}
                    hint="Optional — only needed for internet-facing load balancers."
                  >
                    <Input
                      id={`${uid}-public-subnets`}
                      value={network.publicSubnets}
                      onChange={(e) => patchNetwork({ publicSubnets: e.target.value })}
                      placeholder="subnet-0c7d8e9f"
                      className="font-mono"
                    />
                  </Field>
                </>
              )}

              {netErrors.group && (
                <p className="text-[11px] leading-relaxed text-destructive">{netErrors.group}</p>
              )}
            </Disclosure>

            <Disclosure
              label="System node group"
              summary={systemNodesSummary(systemNodes)}
              open={showNodes}
              onToggle={() => setShowNodes(!showNodes)}
            >
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                The group that hosts the cluster addons. Tenant workloads get their own groups, so
                this sizes the control surface rather than the capacity.
              </p>
              <Field
                label="Instance Types"
                htmlFor={`${uid}-instance-types`}
                hint="Comma-separated, in preference order."
              >
                <Input
                  id={`${uid}-instance-types`}
                  value={systemNodes.instanceTypes}
                  onChange={(e) => patchNodes({ instanceTypes: e.target.value })}
                  placeholder={FLEET_DEFAULTS.instanceTypes.join(', ')}
                  className="font-mono"
                />
              </Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Min Size" htmlFor={`${uid}-min-size`} error={nodeErrors.minSize}>
                  <Input
                    id={`${uid}-min-size`}
                    value={systemNodes.minSize}
                    onChange={(e) => patchNodes({ minSize: e.target.value })}
                    placeholder={String(FLEET_DEFAULTS.minSize)}
                    className="font-mono"
                  />
                </Field>
                <Field label="Max Size" htmlFor={`${uid}-max-size`} error={nodeErrors.maxSize}>
                  <Input
                    id={`${uid}-max-size`}
                    value={systemNodes.maxSize}
                    onChange={(e) => patchNodes({ maxSize: e.target.value })}
                    placeholder={String(FLEET_DEFAULTS.maxSize)}
                    className="font-mono"
                  />
                </Field>
                <Field
                  label="Desired Size"
                  htmlFor={`${uid}-desired-size`}
                  error={nodeErrors.desiredSize}
                >
                  <Input
                    id={`${uid}-desired-size`}
                    value={systemNodes.desiredSize}
                    onChange={(e) => patchNodes({ desiredSize: e.target.value })}
                    placeholder={String(FLEET_DEFAULTS.desiredSize)}
                    className="font-mono"
                  />
                </Field>
                <Field
                  label="Disk Size (GiB)"
                  htmlFor={`${uid}-disk-size`}
                  error={nodeErrors.diskSize}
                >
                  <Input
                    id={`${uid}-disk-size`}
                    value={systemNodes.diskSize}
                    onChange={(e) => patchNodes({ diskSize: e.target.value })}
                    placeholder={String(FLEET_DEFAULTS.diskSize)}
                    className="font-mono"
                  />
                </Field>
              </div>
              {nodeErrors.group && (
                <p className="text-[11px] leading-relaxed text-destructive">{nodeErrors.group}</p>
              )}
            </Disclosure>

            <Field label="Lifetime (days)" htmlFor={`${uid}-ttl-days`} error={ttlError}>
              <Input
                id={`${uid}-ttl-days`}
                value={ttlDays}
                onChange={(e) => setTtlDays(e.target.value)}
                placeholder="Blank — the cluster stays"
                className="font-mono"
              />
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                Above 0 tags the cluster ephemeral, and the hub reaper deletes it that many days
                after creation — the cluster and everything running on it, without asking again.{' '}
                <span className="text-foreground">{ttlSummary(ttlDays)}</span>.
              </p>
            </Field>
          </div>
        )}
      </DrawerBody>

      <DrawerFooter>
        {ordered ? (
          <>
            <Button variant="ghost" size="sm" onClick={handleClose}>
              Done
            </Button>
            <Button
              size="sm"
              onClick={() => {
                navigate('/ops');
                handleClose();
              }}
            >
              Watch in Ops
            </Button>
          </>
        ) : (
          <>
            <Button variant="ghost" size="sm" onClick={handleClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => orderMutation.mutate()}
              disabled={!canSubmit || orderMutation.isPending}
            >
              {orderMutation.isPending ? 'Provisioning…' : 'Provision Cluster'}
            </Button>
          </>
        )}
      </DrawerFooter>
    </Drawer>
  );
}

function Field({
  label,
  htmlFor,
  error,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error?: string | null;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label htmlFor={htmlFor} className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
      </label>
      {children}
      {error ? (
        <p className="text-[11px] text-destructive mt-1">{error}</p>
      ) : (
        hint && <p className="text-[11px] text-muted-foreground/70 mt-1">{hint}</p>
      )}
    </div>
  );
}

// A section that carries the fleet defaults when nobody opens it. The summary is
// what those defaults come to, shown collapsed and expanded both: collapsed it is
// the whole story, expanded it reads back the effective values as the fields
// change, including the ones still blank.
function Disclosure({
  label,
  summary,
  open,
  onToggle,
  children,
}: {
  label: string;
  summary: string;
  open: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-baseline gap-1.5 text-left text-xs text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
      >
        {open ? (
          <ChevronDown className="w-3 h-3 shrink-0 translate-y-0.5" />
        ) : (
          <ChevronRight className="w-3 h-3 shrink-0 translate-y-0.5" />
        )}
        <span className="font-medium shrink-0">{label}</span>
        <span className="text-[11px] text-muted-foreground/60">{summary}</span>
      </button>
      {open && <div className="mt-3 space-y-3">{children}</div>}
    </div>
  );
}

function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn('text-foreground', mono && 'font-mono')}>{value}</dd>
    </>
  );
}
