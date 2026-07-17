import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { Account, User } from '@/api/models';
import { ClusterProvisionDrawer } from './ClusterProvisionDrawer';
import { parseCidrList } from '@/lib/cidr';

// Mock the api client at the module boundary (openapi-fetch binds fetch at
// import, so network-level interception is brittle).
const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

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

describe('parseCidrList', () => {
  it('splits on commas, trims, and drops empties', () => {
    expect(parseCidrList(' 203.0.113.0/24, 198.51.100.7/32 ,')).toEqual([
      '203.0.113.0/24',
      '198.51.100.7/32',
    ]);
    expect(parseCidrList('')).toEqual([]);
  });
});
