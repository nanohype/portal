import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User } from '@/api/models';
import { ApprovalPanel } from './ApprovalPanel';

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

beforeEach(() => {
  vi.clearAllMocks();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/auth/me')
      return Promise.resolve({ data: useAuthStore.getState().user, error: undefined });
    return Promise.resolve({ data: { data: [], total: 0, page: 1, per_page: 50 } });
  });
});

// POST .../approvals sits at ActionApplyProd and is org-scoped on purpose:
// signing off a gated apply is not something a per-workspace grant hands out.
// The buttons have to say the same thing, or the operator's only feedback is a
// toast reading "Failed to submit approval".
describe('ApprovalPanel decision controls', () => {
  it('offers approve and reject to an admin', async () => {
    setOrgRole('admin');
    renderWithClient(<ApprovalPanel workspaceId="ws-1" runId="run-1" runStatus="planned" />);

    expect(await screen.findByRole('button', { name: /approve & apply/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /reject/i })).toBeInTheDocument();
  });

  it('offers neither to an operator, and points them at the apply they do have', async () => {
    setOrgRole('operator');
    renderWithClient(<ApprovalPanel workspaceId="ws-1" runId="run-1" runStatus="planned" />);

    await screen.findByText(/approving a plan is an admin action/i);
    expect(screen.queryByRole('button', { name: /approve & apply/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /reject/i })).toBeNull();
  });

  it('tells an operator what a gated run is waiting on', async () => {
    setOrgRole('operator');
    renderWithClient(
      <ApprovalPanel workspaceId="ws-1" runId="run-1" runStatus="awaiting_approval" />,
    );

    await screen.findByText(/waiting on an admin to approve it/i);
    expect(screen.queryByRole('button', { name: /approve & apply/i })).toBeNull();
  });

  it('shows no decision controls at all once the run is past the decision', async () => {
    setOrgRole('admin');
    renderWithClient(<ApprovalPanel workspaceId="ws-1" runId="run-1" runStatus="applied" />);

    await screen.findByText(/no approvals required/i);
    expect(screen.queryByRole('button', { name: /approve & apply/i })).toBeNull();
  });
});
