import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { Cluster, User } from '@/api/models';
import { FleetOverview } from './FleetOverview';

const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));
vi.mock('@/hooks/useNavigate', () => ({
  navigate: vi.fn(),
  useLocation: () => '/',
}));

function cl(p: Partial<Cluster>): Cluster {
  return {
    connection_status: 'connected',
    argocd_health_status: '',
    control_plane_status: '',
    k8s_version: '1.30',
    environment: 'production',
    ...p,
  } as Cluster;
}

function list<T>(items: T[]) {
  return { data: { data: items, total: items.length, page: 1, per_page: 100 } };
}

function mock(clusters: Cluster[]) {
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/clusters') return Promise.resolve(list(clusters));
    if (path === '/cluster-orders') return Promise.resolve(list([]));
    return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
  });
}

function setRole(role: string) {
  useAuthStore.setState({ token: null, user: { id: 'u1', role } as User, isAuthenticated: true });
}

beforeEach(() => {
  vi.clearAllMocks();
  setRole('admin');
});

describe('FleetOverview', () => {
  it('shows a green verdict for an all-healthy fleet', async () => {
    mock([cl({}), cl({})]);
    renderWithClient(<FleetOverview />);
    expect(await screen.findByText(/all 2 clusters healthy/i)).toBeInTheDocument();
  });

  it('flags how many clusters need attention', async () => {
    mock([cl({}), cl({ connection_status: 'failed' }), cl({ control_plane_status: 'FAILED' })]);
    renderWithClient(<FleetOverview />);
    expect(await screen.findByText(/2 of 3 clusters need attention/i)).toBeInTheDocument();
  });

  it('renders the version spread', async () => {
    mock([cl({ k8s_version: '1.30' }), cl({ k8s_version: '1.29' })]);
    renderWithClient(<FleetOverview />);
    expect(await screen.findByText('1.30')).toBeInTheDocument();
    expect(screen.getByText('1.29')).toBeInTheDocument();
  });

  // Vend orders are an admin-only read on the API. A non-admin renders the
  // fleet without them rather than firing a request that comes back 403.
  it('does not request vend orders as a non-admin', async () => {
    setRole('operator');
    mock([cl({})]);
    renderWithClient(<FleetOverview />);
    expect(await screen.findByText(/all 1 cluster healthy/i)).toBeInTheDocument();
    expect(apiMock.GET).toHaveBeenCalledWith('/clusters', expect.anything());
    expect(apiMock.GET).not.toHaveBeenCalledWith('/cluster-orders');
  });
});
