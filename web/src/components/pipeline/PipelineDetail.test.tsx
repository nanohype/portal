import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User } from '@/api/models';
import { PipelineDetail } from './PipelineDetail';

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

const PIPELINE = {
  pipeline: {
    id: 'pipe-1',
    name: 'eks-gitops-prereqs',
    description: '',
    created_at: new Date().toISOString(),
  },
  stages: [],
};

const VARIABLE = {
  id: 'var-1',
  pipeline_id: 'pipe-1',
  key: 'aws_region',
  value: 'us-west-2',
  sensitive: false,
  category: 'terraform',
  description: '',
};

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
    if (path === '/pipelines/{pipelineId}') return Promise.resolve({ data: PIPELINE });
    if (path === '/pipelines/{pipelineId}/variables')
      return Promise.resolve({ data: { data: [VARIABLE], total: 1, page: 1, per_page: 50 } });
    return Promise.resolve({ data: { data: [], total: 0, page: 1, per_page: 20 } });
  });
});

// A pipeline variable lands in the worker's tfvars file and process environment
// for every stage, so the API holds the writes at ActionManageVars — admin. The
// tab has to hold them at the same bar or an operator fills in a form whose
// submit is refused.
describe('PipelineDetail variables tab', () => {
  it('offers no variable writes to an operator, and says why', async () => {
    setOrgRole('operator');
    renderWithClient(<PipelineDetail pipelineId="pipe-1" />);

    await userEvent.click(await screen.findByRole('tab', { name: /variables/i }));

    expect(await screen.findByText(/editing them is an admin action/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /add variable/i })).toBeNull();
    expect(screen.getByText('aws_region')).toBeInTheDocument();
  });

  it('gives an admin the full variable surface', async () => {
    setOrgRole('admin');
    renderWithClient(<PipelineDetail pipelineId="pipe-1" />);

    await userEvent.click(await screen.findByRole('tab', { name: /variables/i }));

    expect(await screen.findByRole('button', { name: /add variable/i })).toBeInTheDocument();
    expect(screen.queryByText(/editing them is an admin action/i)).toBeNull();
  });
});
