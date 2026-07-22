import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User } from '@/api/models';
import { WorkspaceList } from './WorkspaceList';

const { apiMock, navigateMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
  navigateMock: vi.fn(),
}));
vi.mock('@/api/client', () => ({ api: apiMock }));
vi.mock('@/hooks/useNavigate', () => ({
  navigate: (to: string) => navigateMock(to),
  useLocation: () => '/',
}));

function setOrgRole(role: string) {
  useAuthStore.setState({
    token: 'test-token',
    user: { id: 'u1', role } as User,
    isAuthenticated: true,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/auth/me')
      return Promise.resolve({ data: useAuthStore.getState().user, error: undefined });
    return Promise.resolve({ data: { data: [], total: 0, page: 1, per_page: 20 } });
  });
});

// The empty state is the first screen a second user sees on a fresh install,
// and POST /workspaces is operator+. Offering "create your first workspace" to
// a viewer is offering the one action the API is certain to refuse.
describe('WorkspaceList empty state', () => {
  it('offers no create button to a viewer, and says what it takes', async () => {
    setOrgRole('viewer');
    renderWithClient(<WorkspaceList />);

    await screen.findByText(/creating a workspace takes an operator role or higher/i);
    expect(screen.queryByRole('button', { name: /create workspace/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /new workspace/i })).toBeNull();
  });

  it('keeps the create path for an operator', async () => {
    setOrgRole('operator');
    renderWithClient(<WorkspaceList />);

    expect(await screen.findByRole('button', { name: /create workspace/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /new workspace/i })).toBeInTheDocument();
  });
});
