import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { ClusterOperation, User } from '@/api/models';
import { ClusterDetail } from './ClusterDetail';

// The api client binds globalThis.fetch at import, which makes network-level
// interception brittle; mock it at the module boundary. navigate() reaches the
// real router ref and confirm() comes from a provider we don't mount here.
const { apiMock, navigateMock, confirmMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
  navigateMock: vi.fn(),
  confirmMock: vi.fn(),
}));
vi.mock('@/api/client', () => ({ api: apiMock }));
vi.mock('@/hooks/useNavigate', () => ({
  navigate: (to: string) => navigateMock(to),
  useLocation: () => '/',
}));
vi.mock('@/components/ui/confirm-context', () => ({
  useConfirm: () => confirmMock,
}));

const CLUSTER = {
  id: 'c1',
  name: 'prod-eks',
  description: '',
  environment: 'production',
  account_id: 'acct-1',
  api_endpoint: 'https://eks.example',
  region: 'us-west-2',
  connection_status: 'connected',
  node_count: 3,
};

const PROVISION_OP = {
  id: 'op1',
  name: 'prod-eks',
  environment: 'production',
  team: 'platform',
  operation: 'provision',
  status: 'active',
  vend_phases: {},
} as unknown as ClusterOperation;

// An in-flight teardown — a deprovision that hasn't reached a terminal status.
// Its presence is what makes Unwedge relevant (the Workspace can be stuck).
const ACTIVE_DEPROVISION_OP = {
  id: 'op2',
  name: 'prod-eks',
  environment: 'production',
  team: 'platform',
  operation: 'deprovision',
  status: 'committed',
  vend_phases: {},
} as unknown as ClusterOperation;

function list<T>(items: T[]) {
  return { data: { data: items, total: items.length, page: 1, per_page: 50 } };
}

function setRole(role: string) {
  useAuthStore.setState({
    token: 'test-token',
    user: { id: 'u1', role } as User,
    isAuthenticated: true,
  });
}

// ops controls what the cluster-orders/operations query returns (the provision
// op carries the team; absent = a hand-registered cluster).
function mockApi(ops: ClusterOperation[]) {
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/clusters/{clusterId}')
      return Promise.resolve({ data: CLUSTER, error: undefined });
    if (path === '/cluster-orders/{environment}/{name}/operations')
      return Promise.resolve(list(ops));
    return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
  });
  apiMock.DELETE.mockResolvedValue({ error: undefined });
}

beforeEach(() => {
  vi.clearAllMocks();
  mockApi([PROVISION_OP]);
});

describe('ClusterDetail RBAC', () => {
  it('hides the lifecycle actions and locks the fields for a viewer', async () => {
    setRole('viewer');
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole('heading', { name: 'prod-eks' });
    expect(screen.queryByRole('button', { name: /deprovision/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /remove from portal/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /test connection/i })).toBeNull();
    expect(screen.getByDisplayValue('https://eks.example')).toBeDisabled();
  });

  it('exposes the actions and enables the fields for an admin', async () => {
    setRole('admin');
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole('heading', { name: 'prod-eks' });
    expect(await screen.findByRole('button', { name: /deprovision/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /remove from portal/i })).toBeInTheDocument();
    expect(screen.getByDisplayValue('https://eks.example')).toBeEnabled();
  });
});

describe('ClusterDetail deprovision', () => {
  it('tears down the real cluster only after confirm, passing the team', async () => {
    setRole('admin');
    confirmMock.mockResolvedValue(true);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await userEvent.click(await screen.findByRole('button', { name: /deprovision/i }));

    expect(confirmMock).toHaveBeenCalledOnce();
    await waitFor(() => expect(apiMock.DELETE).toHaveBeenCalledOnce());
    const [path, opts] = apiMock.DELETE.mock.calls[0];
    expect(path).toBe('/cluster-orders/{environment}/{name}');
    expect(opts.params.path).toEqual({
      environment: 'production',
      name: 'prod-eks',
    });
    expect(opts.params.query).toEqual({ team: 'platform' });
    // Deprovision must NOT navigate away — you stay to watch the teardown.
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it('does not tear down when confirm is dismissed', async () => {
    setRole('admin');
    confirmMock.mockResolvedValue(false);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await userEvent.click(await screen.findByRole('button', { name: /deprovision/i }));

    expect(confirmMock).toHaveBeenCalledOnce();
    await Promise.resolve();
    expect(apiMock.DELETE).not.toHaveBeenCalled();
  });

  it('offers no Deprovision for a hand-registered cluster (no provision op)', async () => {
    setRole('admin');
    mockApi([]); // no provision op → no team → can't deprovision
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole('heading', { name: 'prod-eks' });
    expect(screen.getByRole('button', { name: /remove from portal/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /deprovision/i })).toBeNull();
  });
});

describe('ClusterDetail unwedge (break-glass)', () => {
  it('offers Unwedge to an owner mid-teardown and posts with the team after a typed confirm', async () => {
    setRole('owner');
    mockApi([PROVISION_OP, ACTIVE_DEPROVISION_OP]);
    apiMock.POST.mockResolvedValue({ error: undefined });
    confirmMock.mockResolvedValue(true);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await userEvent.click(await screen.findByRole('button', { name: /unwedge/i }));

    // Guarded by a type-the-cluster-name confirm so a misclick can't trigger it.
    expect(confirmMock).toHaveBeenCalledOnce();
    expect(confirmMock.mock.calls[0][0]).toMatchObject({
      requireText: 'prod-eks',
    });

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    const [path, opts] = apiMock.POST.mock.calls[0];
    expect(path).toBe('/cluster-orders/{environment}/{name}/unwedge');
    expect(opts.params.path).toEqual({
      environment: 'production',
      name: 'prod-eks',
    });
    expect(opts.params.query).toEqual({ team: 'platform' });
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it("hides Unwedge from an admin — it's owner-only — even mid-teardown", async () => {
    setRole('admin');
    mockApi([PROVISION_OP, ACTIVE_DEPROVISION_OP]);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole('heading', { name: 'prod-eks' });
    expect(screen.queryByRole('button', { name: /unwedge/i })).toBeNull();
  });

  it('hides Unwedge when no teardown is in flight', async () => {
    setRole('owner');
    mockApi([PROVISION_OP]); // provision only → nothing to unwedge
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole('heading', { name: 'prod-eks' });
    expect(screen.queryByRole('button', { name: /unwedge/i })).toBeNull();
  });
});

describe('ClusterDetail remove-from-portal', () => {
  it('drops the record (DELETE /clusters) and navigates away after confirm', async () => {
    setRole('admin');
    confirmMock.mockResolvedValue(true);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await userEvent.click(await screen.findByRole('button', { name: /remove from portal/i }));

    expect(confirmMock).toHaveBeenCalledOnce();
    await waitFor(() => expect(apiMock.DELETE).toHaveBeenCalledOnce());
    expect(apiMock.DELETE.mock.calls[0][0]).toBe('/clusters/{clusterId}');
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/clusters'));
  });
});
