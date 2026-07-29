import { describe, expect, it } from 'vitest';
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
} from './cluster-order';
import { parseCommaList } from './list';

const net = (over: Partial<NetworkForm> = {}): NetworkForm => ({ ...emptyNetworkForm, ...over });
const nodes = (over: Partial<SystemNodesForm> = {}): SystemNodesForm => ({
  ...emptySystemNodesForm,
  ...over,
});

describe('buildNetwork — the discriminated union', () => {
  it('sends nothing when the create side is untouched', () => {
    // The fleet default said out loud is not the same manifest as the fleet
    // default: one implies someone chose these numbers.
    expect(buildNetwork(net())).toBeUndefined();
  });

  it('sends only the create block in create mode', () => {
    const got = buildNetwork(net({ vpcCidr: '10.4.0.0/16', maxAzs: '2' }));
    expect(got).toEqual({ mode: 'create', create: { vpc_cidr: '10.4.0.0/16', max_azs: 2 } });
  });

  // The load-bearing property. `clusterspec.Validate` refuses the sub-object that
  // does not match the mode, so a leaked branch is a 400 on every submit — and the
  // reason the builder discriminates rather than the state being cleared on flip.
  it('never leaks the adopt branch into a create-mode order', () => {
    const flipped = net({
      mode: 'create',
      vpcCidr: '10.4.0.0/16',
      vpcId: 'vpc-0abc',
      privateSubnets: 'subnet-0a, subnet-0b',
    });
    expect(buildNetwork(flipped)).toEqual({ mode: 'create', create: { vpc_cidr: '10.4.0.0/16' } });
  });

  it('never leaks the create branch into an adopt-mode order', () => {
    const flipped = net({
      mode: 'adopt',
      vpcCidr: '10.4.0.0/16',
      ipamPoolId: 'ipam-pool-0abc',
      centralizedEgress: true,
      vpcId: 'vpc-0abc',
      privateSubnets: 'subnet-0a',
    });
    expect(buildNetwork(flipped)).toEqual({
      mode: 'adopt',
      adopt: { vpc_id: 'vpc-0abc', subnet_ids: { private: ['subnet-0a'] } },
    });
  });

  it('omits the public subnet list when none are given', () => {
    const got = buildNetwork(
      net({ mode: 'adopt', vpcId: 'vpc-0abc', privateSubnets: 'subnet-0a' }),
    );
    expect(got?.adopt?.subnet_ids).not.toHaveProperty('public');
  });

  it('carries public subnets when they are', () => {
    const got = buildNetwork(
      net({
        mode: 'adopt',
        vpcId: 'vpc-0abc',
        privateSubnets: 'subnet-0a',
        publicSubnets: 'subnet-0c, subnet-0d',
      }),
    );
    expect(got?.adopt?.subnet_ids?.public).toEqual(['subnet-0c', 'subnet-0d']);
  });

  // false is the XRD default, so sending it explicitly adds a line to the
  // manifest that says nothing its absence does not.
  it('omits centralized egress when off and sends it when on', () => {
    expect(buildNetwork(net({ transitGatewayId: 'tgw-0abc' }))?.create).toEqual({
      transit_gateway_id: 'tgw-0abc',
    });
    expect(
      buildNetwork(net({ transitGatewayId: 'tgw-0abc', centralizedEgress: true }))?.create,
    ).toEqual({ transit_gateway_id: 'tgw-0abc', centralized_egress: true });
  });

  it('drops a malformed number rather than sending NaN', () => {
    expect(buildNetwork(net({ maxAzs: 'three' }))).toBeUndefined();
  });
});

describe('networkErrors — the refusals clusterspec.Validate would make', () => {
  it('accepts an untouched form', () => {
    expect(networkErrors(net())).toEqual({});
  });

  it('rejects a CIDR that is not one', () => {
    expect(networkErrors(net({ vpcCidr: '10.4.0.0' })).vpcCidr).toMatch(/must be a cidr/i);
  });

  it('refuses an IPAM pool alongside a chosen CIDR', () => {
    expect(
      networkErrors(net({ ipamPoolId: 'ipam-pool-0abc', vpcCidr: '10.4.0.0/16' })).group,
    ).toMatch(/mutually exclusive/i);
  });

  // The exclusion is written against the default: leaving the CIDR alone is how
  // an order says "I did not pick one".
  it('allows an IPAM pool with the default CIDR left in place', () => {
    expect(
      networkErrors(net({ ipamPoolId: 'ipam-pool-0abc', vpcCidr: FLEET_DEFAULTS.vpcCidr })),
    ).toEqual({});
  });

  it('holds the IPAM netmask to 16..20, with 0 meaning literal', () => {
    expect(networkErrors(net({ ipamNetmaskLength: '15' })).ipamNetmaskLength).toMatch(/16 and 20/);
    expect(networkErrors(net({ ipamNetmaskLength: '21' })).ipamNetmaskLength).toMatch(/16 and 20/);
    expect(networkErrors(net({ ipamNetmaskLength: '18' }))).toEqual({});
    expect(networkErrors(net({ ipamNetmaskLength: '0' }))).toEqual({});
    // Reported as a number problem rather than a range one — /slash-18 is not
    // out of range, it is not a length.
    expect(networkErrors(net({ ipamNetmaskLength: '/18' })).ipamNetmaskLength).toMatch(
      /whole number/i,
    );
  });

  it('requires a transit gateway for centralized egress', () => {
    expect(networkErrors(net({ centralizedEgress: true })).transitGatewayId).toMatch(/required/i);
    expect(networkErrors(net({ centralizedEgress: true, transitGatewayId: 'tgw-0abc' }))).toEqual(
      {},
    );
  });

  it('rejects a non-integer count', () => {
    expect(networkErrors(net({ maxAzs: '-1' })).maxAzs).toMatch(/whole number/i);
    expect(networkErrors(net({ natGateways: '1.5' })).natGateways).toMatch(/whole number/i);
    // Zero NAT gateways is the point of centralized egress, so it is not an error.
    expect(networkErrors(net({ natGateways: '0' }))).toEqual({});
  });

  it('requires a VPC and private subnets in adopt mode', () => {
    const e = networkErrors(net({ mode: 'adopt' }));
    expect(e.vpcId).toMatch(/required/i);
    expect(e.privateSubnets).toMatch(/nodes land/i);
  });

  it('catches a name typed where an id belongs', () => {
    const e = networkErrors(
      net({
        mode: 'adopt',
        vpcId: 'shared-vpc',
        privateSubnets: 'private-a',
        publicSubnets: 'subnet-0c, public-b',
      }),
    );
    expect(e.vpcId).toMatch(/vpc-/);
    expect(e.privateSubnets).toMatch(/subnet-/);
    expect(e.publicSubnets).toMatch(/subnet-/);
  });

  it('accepts a complete adopt order', () => {
    expect(
      networkErrors(
        net({
          mode: 'adopt',
          vpcId: 'vpc-0abc',
          privateSubnets: 'subnet-0a, subnet-0b',
          publicSubnets: 'subnet-0c',
        }),
      ),
    ).toEqual({});
  });

  // Mode-scoped, like the builder: a half-typed create block does not block an
  // adopt order, and vice versa.
  it('ignores the inactive branch', () => {
    expect(
      networkErrors(
        net({ mode: 'adopt', vpcId: 'vpc-0abc', privateSubnets: 'subnet-0a', vpcCidr: 'nonsense' }),
      ),
    ).toEqual({});
    expect(networkErrors(net({ mode: 'create', vpcId: 'nonsense' }))).toEqual({});
  });
});

describe('buildSystemNodes', () => {
  it('sends nothing when untouched', () => {
    expect(buildSystemNodes(nodes())).toBeUndefined();
  });

  it('sends only what was filled in', () => {
    expect(buildSystemNodes(nodes({ minSize: '3', maxSize: '9' }))).toEqual({
      min_size: 3,
      max_size: 9,
    });
  });

  it('splits the instance type list', () => {
    expect(buildSystemNodes(nodes({ instanceTypes: 'm7g.2xlarge, c7g.xlarge' }))).toEqual({
      instance_types: ['m7g.2xlarge', 'c7g.xlarge'],
    });
  });

  it('sends a zero size, which means a group scaled to zero', () => {
    expect(buildSystemNodes(nodes({ minSize: '0' }))).toEqual({ min_size: 0 });
  });

  it('sends the desired size and the disk size', () => {
    expect(buildSystemNodes(nodes({ desiredSize: '4', diskSize: '300' }))).toEqual({
      desired_size: 4,
      disk_size: 300,
    });
  });
});

describe('systemNodesErrors — the sizing rule', () => {
  it('accepts an untouched form', () => {
    expect(systemNodesErrors(nodes())).toEqual({});
  });

  it('accepts a coherent group', () => {
    expect(
      systemNodesErrors(nodes({ minSize: '3', maxSize: '9', desiredSize: '3', diskSize: '200' })),
    ).toEqual({});
  });

  it('refuses min above max', () => {
    expect(systemNodesErrors(nodes({ minSize: '9', maxSize: '3' })).group).toMatch(/exceeds max/i);
  });

  it('refuses a desired size outside the range', () => {
    expect(
      systemNodesErrors(nodes({ minSize: '3', maxSize: '9', desiredSize: '1' })).group,
    ).toMatch(/between min/i);
    expect(
      systemNodesErrors(nodes({ minSize: '3', maxSize: '9', desiredSize: '12' })).group,
    ).toMatch(/between min/i);
  });

  // The reason the comparison uses effective values: this order is
  // self-consistent as typed and incoherent against the default it did not
  // override, which is the group EKS is actually asked for.
  it('compares against the fleet default a blank field takes', () => {
    const e = systemNodesErrors(nodes({ minSize: '9' }));
    expect(e.group).toMatch(new RegExp(`max ${FLEET_DEFAULTS.maxSize}`));
    // ...and says where that number came from.
    expect(e.group).toMatch(/blank fields take the fleet default/i);
  });

  it('does not explain defaults it did not use', () => {
    const e = systemNodesErrors(nodes({ minSize: '9', maxSize: '3', desiredSize: '3' }));
    expect(e.group).not.toMatch(/blank fields/i);
  });

  it('holds max and disk to a floor that can run a node', () => {
    expect(systemNodesErrors(nodes({ maxSize: '0' })).maxSize).toMatch(/at least 1/i);
    expect(systemNodesErrors(nodes({ diskSize: '0' })).diskSize).toMatch(/at least 1/i);
  });

  it('reports a malformed number instead of comparing it', () => {
    const e = systemNodesErrors(nodes({ minSize: 'two', maxSize: '3' }));
    expect(e.minSize).toMatch(/whole number/i);
    expect(e.group).toBeUndefined();
  });
});

describe('buildTtlDays', () => {
  it('treats blank and zero alike — both are a cluster that stays', () => {
    expect(buildTtlDays('')).toBeUndefined();
    expect(buildTtlDays('0')).toBeUndefined();
  });

  it('sends a positive day count', () => {
    expect(buildTtlDays('7')).toBe(7);
  });

  it('reports a malformed value rather than sending it', () => {
    expect(buildTtlDays('a week')).toBeUndefined();
    expect(ttlDaysError('a week')).toMatch(/whole days/i);
    expect(ttlDaysError('7')).toBeUndefined();
    expect(ttlDaysError('')).toBeUndefined();
  });
});

// A collapsed section still has to say what the cluster will be, or "I didn't
// touch networking" and "I don't know what networking does" look identical.
describe('summaries state the effective cluster, defaults included', () => {
  it('describes an untouched network with the fleet defaults', () => {
    expect(networkSummary(net())).toBe(
      `New VPC · ${FLEET_DEFAULTS.vpcCidr} · ${FLEET_DEFAULTS.maxAzs} AZs · 1 NAT gateway`,
    );
  });

  it('describes an IPAM allocation by pool rather than by CIDR', () => {
    expect(
      networkSummary(net({ ipamPoolId: 'ipam-pool-0abc', ipamNetmaskLength: '18' })),
    ).toContain('IPAM /18');
  });

  it('counts AZs and NAT gateways in the singular where they are one', () => {
    expect(networkSummary(net({ maxAzs: '1', natGateways: '0' }))).toBe(
      `New VPC · ${FLEET_DEFAULTS.vpcCidr} · 1 AZ · 0 NAT gateways`,
    );
  });

  it('describes centralized egress instead of counting NAT gateways', () => {
    expect(
      networkSummary(net({ centralizedEgress: true, transitGatewayId: 'tgw-0abc' })),
    ).toContain('egress via transit gateway');
  });

  it('names the adopted VPC and counts the private subnets', () => {
    expect(
      networkSummary(net({ mode: 'adopt', vpcId: 'vpc-0abc', privateSubnets: 'subnet-0a' })),
    ).toBe('Adopting vpc-0abc · 1 private subnet');
    expect(
      networkSummary(
        net({ mode: 'adopt', vpcId: 'vpc-0abc', privateSubnets: 'subnet-0a, subnet-0b' }),
      ),
    ).toBe('Adopting vpc-0abc · 2 private subnets');
  });

  it('describes an untouched node group with the fleet defaults', () => {
    expect(systemNodesSummary(nodes())).toBe(
      `${FLEET_DEFAULTS.instanceTypes.join(', ')} · ${FLEET_DEFAULTS.minSize}–${FLEET_DEFAULTS.maxSize} nodes, ${FLEET_DEFAULTS.desiredSize} desired · ${FLEET_DEFAULTS.diskSize} GiB`,
    );
  });

  it('describes an overridden node group', () => {
    expect(
      systemNodesSummary(
        nodes({ instanceTypes: 'c7g.4xlarge', minSize: '4', maxSize: '12', diskSize: '500' }),
      ),
    ).toBe('c7g.4xlarge · 4–12 nodes, 2 desired · 500 GiB');
  });

  it('states the reaper consequence, not just the number', () => {
    expect(ttlSummary('')).toBe('Persistent');
    expect(ttlSummary('1')).toBe('Reaped 1 day after creation');
    expect(ttlSummary('7')).toBe('Reaped 7 days after creation');
  });
});

describe('parseCommaList', () => {
  it('splits on commas, trims, and drops empties', () => {
    expect(parseCommaList(' subnet-0a, subnet-0b ,')).toEqual(['subnet-0a', 'subnet-0b']);
    expect(parseCommaList('')).toEqual([]);
  });
});
