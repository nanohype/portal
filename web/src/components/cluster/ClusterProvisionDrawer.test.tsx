import { screen, waitFor } from '@testing-library/react';
import type { UserEvent } from '@testing-library/user-event';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Account, ClusterOrderInput, User } from '@/api/models';
import { FLEET_DEFAULTS } from '@/lib/cluster-order';
import { useAuthStore } from '@/stores/auth';
import { renderWithClient } from '@/test/render';
import { ClusterProvisionDrawer } from './ClusterProvisionDrawer';

// Mock the api client at the module boundary (openapi-fetch binds fetch at
// import, so network-level interception is brittle).
const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

// The order-desk half of the contract check `internal/clusterspec` makes against
// the Cluster XRD, one layer up. That test catches a schema field portal cannot
// express; this one catches an order field nobody can place, which is the same
// failure with a shorter reach: `npm run generate:api` regenerates
// ClusterOrderInput from openapi.yaml, so a field added to the API and forgotten
// on the form has to be classified here or `tsc -b` stops.
type OrderedOnTheDrawer =
  | 'name'
  | 'account'
  | 'region'
  | 'team'
  | 'environment'
  | 'cluster_version'
  | 'endpoint_public_access'
  | 'endpoint_public_access_cidrs'
  | 'observability_tier'
  | 'network'
  | 'system_nodes'
  | 'ttl_days';

// Resolved from the ordering account's row or from portal's own configuration.
// None of them is a choice an operator makes per order, and each is a value only
// portal or landing-zone knows.
type StampedNotOrdered =
  | 'vend_role_arn'
  | 'data_kms_key_arn'
  | 'cluster_permissions_boundary_arn'
  | 'operator_permissions_boundary_arn';

type UnclassifiedOrderFields = Exclude<
  keyof ClusterOrderInput,
  OrderedOnTheDrawer | StampedNotOrdered
>;

// If this line stops compiling, a ClusterOrderInput field is neither on the
// drawer nor listed as stamped. Decide which and write it down — the error names
// the field, as the one value that does not satisfy `never`.
type MustBeEmpty<T extends never> = T;
type _EveryOrderFieldIsAccountedFor = MustBeEmpty<UnclassifiedOrderFields>;

const ACCOUNT = {
  id: 'a1',
  name: 'prod',
  aws_account_id: '111111111111',
  default_region: 'us-west-2',
} as unknown as Account;

// Open the custom Select and click the named option (see the Select combobox
// pattern in TenantCreateModal.test.tsx).
async function choose(user: UserEvent, current: RegExp, option: RegExp) {
  const trigger = (await screen.findByText(current)).closest('[role="combobox"]');
  if (!trigger) throw new Error(`no combobox showing ${current}`);
  await user.click(trigger);
  await user.click(await screen.findByRole('option', { name: option }));
}

// Fill everything except the endpoint fields: account (pre-fills the region
// from its default), name, team.
async function fillRequired(user: UserEvent) {
  await choose(user, /pick an account/i, /prod/i);
  await user.type(screen.getByPlaceholderText('platform'), 'analytics');
  await user.type(screen.getByPlaceholderText('apps'), 'platform');
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.setState({
    token: 'test-token',
    user: { id: 'u1', role: 'admin' } as User,
    isAuthenticated: true,
  });
  apiMock.GET.mockResolvedValue({ data: { data: [] } });
});

describe('ClusterProvisionDrawer private-by-default posture', () => {
  it('defaults the public endpoint toggle off and submits a private order', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'op1', status: 'queued' } });
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    expect(screen.getByRole('checkbox', { name: /public api endpoint/i })).not.toBeChecked();

    await fillRequired(user);
    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    const body = apiMock.POST.mock.calls[0][1].body;
    expect(body.endpoint_public_access).toBe(false);
    expect(body.endpoint_public_access_cidrs).toBeUndefined();
  });

  it('requires a CIDR allowlist once public access is toggled on', async () => {
    const user = userEvent.setup();
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());

    // Toggling public on with no allowlist blocks submit + explains why.
    await user.click(screen.getByRole('checkbox', { name: /public api endpoint/i }));
    expect(submit).toBeDisabled();
    expect(screen.getByText(/required to enable the public endpoint/i)).toBeInTheDocument();

    // A malformed entry keeps it blocked.
    const cidrs = screen.getByPlaceholderText(/203\.0\.113\.0\/24/);
    await user.type(cidrs, '203.0.113.0');
    expect(submit).toBeDisabled();
    expect(screen.getByText(/each entry must be a cidr/i)).toBeInTheDocument();

    // Completing the CIDR unblocks it.
    await user.type(cidrs, '/24');
    await waitFor(() => expect(submit).toBeEnabled());
  });

  it('sends the allowlist with a public order', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'op1', status: 'queued' } });
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    await user.click(screen.getByRole('checkbox', { name: /public api endpoint/i }));
    await user.type(
      screen.getByPlaceholderText(/203\.0\.113\.0\/24/),
      '203.0.113.0/24, 198.51.100.7/32',
    );

    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    const body = apiMock.POST.mock.calls[0][1].body;
    expect(body.endpoint_public_access).toBe(true);
    expect(body.endpoint_public_access_cidrs).toEqual(['203.0.113.0/24', '198.51.100.7/32']);
  });
});

// The three optional blocks. Their rules are covered directly in
// lib/cluster-order.test.ts; what matters here is the wiring — that an untouched
// drawer sends none of them, that the mode flip does not leak the other branch
// into the payload, and that a rule the server would refuse blocks submit here.
describe('ClusterProvisionDrawer optional blocks', () => {
  it('sends no network, node group or lifetime when nobody opens them', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'op1', status: 'queued' } });
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    const body = apiMock.POST.mock.calls[0][1].body;
    expect(body.network).toBeUndefined();
    expect(body.system_nodes).toBeUndefined();
    expect(body.ttl_days).toBeUndefined();
  });

  // A collapsed section is a stated choice, not an unknown.
  it('states the defaults a collapsed section will produce', async () => {
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    // Spelled out rather than composed from FLEET_DEFAULTS: the point is what an
    // operator reads before opening anything, and a summary assembled from the
    // same constants the component uses would agree with itself either way.
    expect(screen.getByText('New VPC · 10.0.0.0/16 · 3 AZs · 1 NAT gateway')).toBeInTheDocument();
    expect(
      screen.getByText('m7g.xlarge, m6g.xlarge · 2–6 nodes, 2 desired · 100 GiB'),
    ).toBeInTheDocument();
  });

  it('sends an adopt order with no create block after switching modes', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'op1', status: 'queued' } });
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    await user.click(screen.getByRole('button', { name: /networking/i }));

    // Type into the create branch first, then switch. clusterspec.Validate
    // refuses the sub-object that does not match the mode, so a leaked create
    // block would 400 every submit.
    await user.type(screen.getByPlaceholderText(FLEET_DEFAULTS.vpcCidr), '10.4.0.0/16');
    await user.click(screen.getByRole('button', { name: /adopt a vpc/i }));
    await user.type(screen.getByPlaceholderText('vpc-0a1b2c3d'), 'vpc-0abc');
    await user.type(
      screen.getByPlaceholderText(/subnet-0a1b2c3d, subnet-0e4f5a6b/),
      'subnet-0a, subnet-0b',
    );

    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    expect(apiMock.POST.mock.calls[0][1].body.network).toEqual({
      mode: 'adopt',
      adopt: { vpc_id: 'vpc-0abc', subnet_ids: { private: ['subnet-0a', 'subnet-0b'] } },
    });
  });

  it('blocks an adopt order with no VPC', async () => {
    const user = userEvent.setup();
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());

    await user.click(screen.getByRole('button', { name: /networking/i }));
    await user.click(screen.getByRole('button', { name: /adopt a vpc/i }));

    expect(submit).toBeDisabled();
    expect(screen.getByText(/the VPC the cluster joins/i)).toBeInTheDocument();
    expect(screen.getByText(/nodes land in these/i)).toBeInTheDocument();
  });

  // The sizing rule has no counterpart at admission — the failure it prevents is
  // an autoscaling one, partway through the vend.
  it('blocks a node group whose desired size sits outside the range', async () => {
    const user = userEvent.setup();
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());

    await user.click(screen.getByRole('button', { name: /system node group/i }));
    await user.type(screen.getByPlaceholderText('6'), '1');

    expect(submit).toBeDisabled();
    expect(screen.getByText(/blank fields take the fleet default/i)).toBeInTheDocument();
  });

  it('sends a lifetime and states the reaper consequence', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'op1', status: 'queued' } });
    renderWithClient(<ClusterProvisionDrawer open onClose={vi.fn()} accounts={[ACCOUNT]} />);

    await fillRequired(user);
    await user.type(screen.getByPlaceholderText(/the cluster stays/i), '7');
    expect(screen.getByText(/Reaped 7 days after creation/)).toBeInTheDocument();

    const submit = screen.getByRole('button', { name: /provision cluster/i });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    expect(apiMock.POST.mock.calls[0][1].body.ttl_days).toBe(7);
  });
});
