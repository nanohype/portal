import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User } from '@/api/models';
import { PipelineRunView } from './PipelineRunView';

const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

function setOrgRole(role: string) {
  useAuthStore.setState({
    token: 'test-token',
    user: { id: 'u1', role } as User,
    isAuthenticated: true,
  });
}

const parkedRun = {
  pipeline_run: {
    id: 'pr-1',
    pipeline_id: 'p-1',
    status: 'running',
    current_stage: 0,
    total_stages: 2,
    created_by: 'u1',
    started_at: '2026-07-21T00:00:00Z',
  },
  stages: [
    {
      id: 'prs-1',
      pipeline_run_id: 'pr-1',
      stage_id: 's-1',
      workspace_id: 'ws-1',
      workspace_name: 'network',
      run_id: 'run-1',
      stage_order: 0,
      status: 'awaiting_approval',
      auto_apply: false,
      on_failure: 'stop',
      created_at: '2026-07-21T00:00:00Z',
      updated_at: '2026-07-21T00:00:00Z',
    },
  ],
};

beforeEach(() => {
  vi.clearAllMocks();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/auth/me')
      return Promise.resolve({ data: useAuthStore.getState().user, error: undefined });
    return Promise.resolve({ data: parkedRun, error: undefined });
  });
});

// A stage parks at awaiting_approval and is signed through the same
// POST /workspaces/{ws}/runs/{run}/approvals ApprovalPanel uses — ActionApplyProd,
// org-scoped. Rendering the buttons to every role made a 403 the only way to
// learn that, on the one screen where a stuck pipeline is diagnosed.
describe('PipelineRunView stage approval controls', () => {
  it('offers approve and reject on a parked stage to an admin', async () => {
    setOrgRole('admin');
    renderWithClient(<PipelineRunView pipelineId="p-1" runId="pr-1" />);

    expect(await screen.findByRole('button', { name: /approve/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /reject/i })).toBeInTheDocument();
  });

  it('offers neither to an operator, and says what the stage is waiting on', async () => {
    setOrgRole('operator');
    renderWithClient(<PipelineRunView pipelineId="p-1" runId="pr-1" />);

    await screen.findByText(/waiting on an admin/i);
    expect(screen.queryByRole('button', { name: /approve/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /reject/i })).toBeNull();
  });

  it('offers neither to a viewer', async () => {
    setOrgRole('viewer');
    renderWithClient(<PipelineRunView pipelineId="p-1" runId="pr-1" />);

    await screen.findByText(/waiting on an admin/i);
    expect(screen.queryByRole('button', { name: /approve/i })).toBeNull();
  });
});
