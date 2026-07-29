import type {
  ClusterOrderNetwork,
  ClusterOrderNetworkCreate,
  ClusterOrderSystemNodes,
} from '@/api/models';
import { CIDR_RE } from './cidr';
import { parseCommaList } from './list';

// The order desk's three optional blocks — the VPC, the system node group, and
// the cluster's lifetime — as form state, payload builders, and the refusals the
// server would make.
//
// They live here rather than in the drawer because each one is a pure function of
// text fields, and because the refusals matter enough to test directly: every
// rule below is also enforced by `clusterspec.Validate`, and a rule the form gets
// wrong turns a submit into a 400 the operator has no path around.

// The eks-fleet Cluster XRD's defaults, mirrored for two purposes: a collapsed
// section states what leaving it alone actually produces, and the sizing
// pre-flight compares the group EKS is asked for rather than the fields the
// operator happened to fill in.
//
// These are advisory. `internal/clusterspec` carries the same numbers and asserts
// them against the vendored schema, and the server is what decides — a copy here
// that fell behind would misdescribe a default, not vend the wrong cluster.
export const FLEET_DEFAULTS = {
  vpcCidr: '10.0.0.0/16',
  maxAzs: 3,
  natGateways: 1,
  instanceTypes: ['m7g.xlarge', 'm6g.xlarge'],
  minSize: 2,
  maxSize: 6,
  desiredSize: 2,
  diskSize: 100,
} as const;

export type NetworkMode = 'create' | 'adopt';

// One flat record holding both branches of the union.
//
// The mode decides which half `buildNetwork` reads, and the unread half is never
// sent. That is what keeps a mode flip from producing an order the server
// refuses: `Validate` rejects the sub-object that does not match the mode rather
// than ignoring it, because an accepted-and-dropped adopt block reads as "the
// order took effect" while the stack builds a fresh VPC. Discriminating in the
// builder rather than clearing state on flip also means switching over to compare
// the two shapes and switching back does not throw away what was typed.
export type NetworkForm = {
  mode: NetworkMode;
  // create
  vpcCidr: string;
  ipamPoolId: string;
  ipamNetmaskLength: string;
  transitGatewayId: string;
  centralizedEgress: boolean;
  maxAzs: string;
  natGateways: string;
  // adopt
  vpcId: string;
  privateSubnets: string;
  publicSubnets: string;
};

export type SystemNodesForm = {
  instanceTypes: string;
  minSize: string;
  maxSize: string;
  desiredSize: string;
  diskSize: string;
};

export const emptyNetworkForm: NetworkForm = {
  mode: 'create',
  vpcCidr: '',
  ipamPoolId: '',
  ipamNetmaskLength: '',
  transitGatewayId: '',
  centralizedEgress: false,
  maxAzs: '',
  natGateways: '',
  vpcId: '',
  privateSubnets: '',
  publicSubnets: '',
};

export const emptySystemNodesForm: SystemNodesForm = {
  instanceTypes: '',
  minSize: '',
  maxSize: '',
  desiredSize: '',
  diskSize: '',
};

// A field error is keyed by the field to fix. `group` carries a conflict between
// two supplied values, where no single field is the wrong one — min/max/desired
// disagreeing, or an IPAM pool alongside a chosen CIDR.
export type NetworkErrors = Partial<Record<keyof NetworkForm | 'group', string>>;
export type SystemNodesErrors = Partial<Record<keyof SystemNodesForm | 'group', string>>;

// undefined = left blank, take the fleet default. null = typed but not a
// non-negative whole number, which the error functions report and the builders
// drop rather than sending NaN.
type IntField = number | undefined | null;

const NOT_AN_INT = 'Whole number, 0 or above';

function intField(raw: string): IntField {
  const s = raw.trim();
  if (s === '') return undefined;
  return /^\d+$/.test(s) ? Number(s) : null;
}

function intOr(field: IntField, fallback: number): number {
  return typeof field === 'number' ? field : fallback;
}

// AWS resource ids are always <type>-<hex>, and the prefix is the half worth
// checking: it separates a real id from a name someone typed from memory, a
// failure that otherwise surfaces deep in the vend. The suffix is left alone — its
// length has changed before, and refusing a valid id is worse than forwarding a
// wrong one the AWS API will reject by name.
function idError(value: string, prefix: string, label: string): string | undefined {
  return value.startsWith(prefix) ? undefined : `Must be ${label} like ${prefix}0a1b2c3d`;
}

function idListError(values: string[], prefix: string, label: string): string | undefined {
  return values.every((v) => v.startsWith(prefix))
    ? undefined
    : `Each entry must be ${label} like ${prefix}0a1b2c3d`;
}

export function networkErrors(form: NetworkForm): NetworkErrors {
  const e: NetworkErrors = {};

  if (form.mode === 'adopt') {
    const vpc = form.vpcId.trim();
    if (vpc === '') e.vpcId = 'Required — the VPC the cluster joins';
    else set(e, 'vpcId', idError(vpc, 'vpc-', 'a VPC id'));

    const priv = parseCommaList(form.privateSubnets);
    if (priv.length === 0) e.privateSubnets = 'Required — nodes land in these';
    else set(e, 'privateSubnets', idListError(priv, 'subnet-', 'a subnet id'));

    const pub = parseCommaList(form.publicSubnets);
    set(e, 'publicSubnets', idListError(pub, 'subnet-', 'a subnet id'));
    return e;
  }

  const cidr = form.vpcCidr.trim();
  if (cidr !== '' && !CIDR_RE.test(cidr)) e.vpcCidr = 'Must be a CIDR like 10.0.0.0/16';

  const pool = form.ipamPoolId.trim();
  if (pool !== '') set(e, 'ipamPoolId', idError(pool, 'ipam-pool-', 'an IPAM pool id'));
  // With a pool the CIDR is drawn from it, so a chosen CIDR is a second answer to
  // the same question. The default reads as "I did not choose one".
  if (pool !== '' && cidr !== '' && cidr !== FLEET_DEFAULTS.vpcCidr) {
    e.group = `An IPAM pool and a chosen VPC CIDR are mutually exclusive — with a pool the CIDR is drawn from it. Clear the CIDR or leave it at ${FLEET_DEFAULTS.vpcCidr}.`;
  }

  const netmask = intField(form.ipamNetmaskLength);
  if (netmask === null) {
    e.ipamNetmaskLength = NOT_AN_INT;
  } else if (typeof netmask === 'number' && netmask !== 0 && (netmask < 16 || netmask > 20)) {
    e.ipamNetmaskLength =
      'Between 16 and 20 — subnets are carved 8 bits smaller, and /28 is the AWS minimum';
  }

  const tgw = form.transitGatewayId.trim();
  if (form.centralizedEgress && tgw === '') {
    e.transitGatewayId = 'Required for centralized egress — private egress routes through it';
  } else if (tgw !== '') {
    set(e, 'transitGatewayId', idError(tgw, 'tgw-', 'a transit gateway id'));
  }

  const maxAzs = intField(form.maxAzs);
  if (maxAzs === null) e.maxAzs = NOT_AN_INT;
  else if (maxAzs === 0) e.maxAzs = 'At least 1 — the cluster needs a subnet to land in';

  if (intField(form.natGateways) === null) e.natGateways = NOT_AN_INT;

  return e;
}

export function systemNodesErrors(form: SystemNodesForm): SystemNodesErrors {
  const e: SystemNodesErrors = {};
  const min = intField(form.minSize);
  const max = intField(form.maxSize);
  const desired = intField(form.desiredSize);
  const disk = intField(form.diskSize);

  if (min === null) e.minSize = NOT_AN_INT;
  if (max === null) e.maxSize = NOT_AN_INT;
  else if (max === 0) e.maxSize = 'At least 1 — a node group that cannot run a node is refused';
  if (desired === null) e.desiredSize = NOT_AN_INT;
  if (disk === null) e.diskSize = NOT_AN_INT;
  else if (disk === 0) e.diskSize = 'At least 1 GiB';

  // Compared as the group EKS is asked for, so an order that sets one size and
  // leaves the rest is checked against the defaults it did not override.
  // Otherwise min_size 9 on its own passes here and the autoscaling API refuses
  // it against the default max of 6 — partway through the vend, after portal has
  // committed the manifest and reported the operation committed.
  if (min !== null && max !== null && desired !== null) {
    const lo = intOr(min, FLEET_DEFAULTS.minSize);
    const hi = intOr(max, FLEET_DEFAULTS.maxSize);
    const want = intOr(desired, FLEET_DEFAULTS.desiredSize);
    const note = unsetNote([
      [min, 'min', FLEET_DEFAULTS.minSize],
      [max, 'max', FLEET_DEFAULTS.maxSize],
      [desired, 'desired', FLEET_DEFAULTS.desiredSize],
    ]);
    if (lo > hi) {
      e.group = `Min ${lo} exceeds max ${hi}${note}`;
    } else if (want < lo || want > hi) {
      e.group = `Desired ${want} must be between min ${lo} and max ${hi}${note}`;
    }
  }

  return e;
}

// The id checks answer undefined for a clean value; assigning only a real message
// keeps `Object.keys(errors).length` meaning "something is wrong".
function set<K extends string>(
  errors: Partial<Record<K, string>>,
  key: K,
  message: string | undefined,
) {
  if (message !== undefined) errors[key] = message;
}

// Names the defaults a comparison leaned on, so the numbers in the message are
// all accounted for.
function unsetNote(fields: [IntField, string, number][]): string {
  const unset = fields
    .filter(([field]) => typeof field !== 'number')
    .map(([, label, value]) => `${label} ${value}`);
  return unset.length === 0 ? '' : ` (blank fields take the fleet default: ${unset.join(', ')})`;
}

export function ttlDaysError(raw: string): string | undefined {
  return intField(raw) === null ? 'Whole days, or blank for a cluster that stays' : undefined;
}

export function buildNetwork(form: NetworkForm): ClusterOrderNetwork | undefined {
  if (form.mode === 'adopt') {
    const publicSubnets = parseCommaList(form.publicSubnets);
    return {
      mode: 'adopt',
      adopt: {
        vpc_id: form.vpcId.trim(),
        subnet_ids: {
          private: parseCommaList(form.privateSubnets),
          ...(publicSubnets.length > 0 ? { public: publicSubnets } : {}),
        },
      },
    };
  }

  const create: ClusterOrderNetworkCreate = {};
  const cidr = form.vpcCidr.trim();
  if (cidr !== '') create.vpc_cidr = cidr;
  const pool = form.ipamPoolId.trim();
  if (pool !== '') create.ipam_pool_id = pool;
  const netmask = intField(form.ipamNetmaskLength);
  if (typeof netmask === 'number') create.ipam_netmask_length = netmask;
  const tgw = form.transitGatewayId.trim();
  if (tgw !== '') create.transit_gateway_id = tgw;
  // false is the default; sending it says nothing its absence does not.
  if (form.centralizedEgress) create.centralized_egress = true;
  const maxAzs = intField(form.maxAzs);
  if (typeof maxAzs === 'number') create.max_azs = maxAzs;
  const nat = intField(form.natGateways);
  if (typeof nat === 'number') create.nat_gateways = nat;

  // An empty create block is the fleet default restated. Sending no network at
  // all renders a CR with no network stanza — the same cluster, and a manifest
  // that does not imply anyone chose these numbers.
  return Object.keys(create).length === 0 ? undefined : { mode: 'create', create };
}

export function buildSystemNodes(form: SystemNodesForm): ClusterOrderSystemNodes | undefined {
  const nodes: ClusterOrderSystemNodes = {};
  const types = parseCommaList(form.instanceTypes);
  if (types.length > 0) nodes.instance_types = types;
  const min = intField(form.minSize);
  if (typeof min === 'number') nodes.min_size = min;
  const max = intField(form.maxSize);
  if (typeof max === 'number') nodes.max_size = max;
  const desired = intField(form.desiredSize);
  if (typeof desired === 'number') nodes.desired_size = desired;
  const disk = intField(form.diskSize);
  if (typeof disk === 'number') nodes.disk_size = disk;
  return Object.keys(nodes).length === 0 ? undefined : nodes;
}

// 0 and absent are the same cluster; absent keeps ttlDays out of the manifest.
export function buildTtlDays(raw: string): number | undefined {
  const days = intField(raw);
  return typeof days === 'number' && days > 0 ? days : undefined;
}

// The summaries are what a collapsed section says. A section nobody opened still
// describes the cluster it will produce, defaults included, so "I didn't touch
// networking" and "I don't know what networking does" are not the same state.

export function networkSummary(form: NetworkForm): string {
  if (form.mode === 'adopt') {
    const count = parseCommaList(form.privateSubnets).length;
    const subnets = count === 1 ? '1 private subnet' : `${count} private subnets`;
    return `Adopting ${form.vpcId.trim() || 'a VPC'} · ${subnets}`;
  }
  const pool = form.ipamPoolId.trim();
  const netmask = intField(form.ipamNetmaskLength);
  const block =
    pool !== ''
      ? `IPAM${typeof netmask === 'number' && netmask !== 0 ? ` /${netmask}` : ''}`
      : form.vpcCidr.trim() || FLEET_DEFAULTS.vpcCidr;
  const azs = intOr(intField(form.maxAzs), FLEET_DEFAULTS.maxAzs);
  const nat = intOr(intField(form.natGateways), FLEET_DEFAULTS.natGateways);
  const egress = form.centralizedEgress
    ? 'egress via transit gateway'
    : `${nat} NAT ${nat === 1 ? 'gateway' : 'gateways'}`;
  return `New VPC · ${block} · ${azs} ${azs === 1 ? 'AZ' : 'AZs'} · ${egress}`;
}

export function systemNodesSummary(form: SystemNodesForm): string {
  const types = parseCommaList(form.instanceTypes);
  const instances = types.length > 0 ? types : FLEET_DEFAULTS.instanceTypes;
  const lo = intOr(intField(form.minSize), FLEET_DEFAULTS.minSize);
  const hi = intOr(intField(form.maxSize), FLEET_DEFAULTS.maxSize);
  const want = intOr(intField(form.desiredSize), FLEET_DEFAULTS.desiredSize);
  const disk = intOr(intField(form.diskSize), FLEET_DEFAULTS.diskSize);
  return `${instances.join(', ')} · ${lo}–${hi} nodes, ${want} desired · ${disk} GiB`;
}

export function ttlSummary(raw: string): string {
  const days = buildTtlDays(raw);
  if (days === undefined) return 'Persistent';
  return `Reaped ${days} ${days === 1 ? 'day' : 'days'} after creation`;
}
