import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User, Workspace } from '@/api/models';
import { WorkspaceSettings } from './WorkspaceSettings';

const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

function workspace(overrides: Partial<Workspace> = {}): Workspace {
  return {
    id: 'ws-1',
    org_id: 'org-1',
    name: 'dev-vpc',
    description: '',
    source: 'vcs',
    repo_url: 'https://example.test/infra.git',
    repo_branch: 'main',
    working_dir: 'modules/vpc',
    tofu_version: '1.11.0',
    environment: 'development',
    auto_apply: false,
    requires_approval: false,
    vcs_trigger_enabled: false,
    locked: false,
    ...overrides,
  } as Workspace;
}

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
    return Promise.resolve({ data: { data: [] }, error: undefined });
  });
});

// Direction decides the bar. Removing the human from an apply is the act that
// sits with signing approvals; adding one back hands out nothing — and it is
// what the 403 on a config another workspace already gates tells an operator to
// do, so the form has to let them do it.
describe('WorkspaceSettings approval-gate controls', () => {
  it('lets an operator gate a workspace that has no gate', () => {
    setOrgRole('operator');
    renderWithClient(<WorkspaceSettings workspace={workspace()} />);

    expect(screen.getByLabelText(/require approval/i)).toBeEnabled();
  });

  it('will not let an operator remove a gate that is already there', () => {
    setOrgRole('operator');
    renderWithClient(<WorkspaceSettings workspace={workspace({ requires_approval: true })} />);

    const gate = screen.getByLabelText(/require approval/i);
    expect(gate).toBeDisabled();
    expect(screen.getByText(/admins turn this off/i)).toBeInTheDocument();
  });

  it('will not let an operator turn auto-apply on', () => {
    setOrgRole('operator');
    renderWithClient(<WorkspaceSettings workspace={workspace()} />);

    expect(screen.getByLabelText(/auto-apply/i)).toBeDisabled();
    expect(screen.getByText(/admins turn this on/i)).toBeInTheDocument();
  });

  it('lets an operator turn auto-apply off again', () => {
    setOrgRole('operator');
    renderWithClient(<WorkspaceSettings workspace={workspace({ auto_apply: true })} />);

    expect(screen.getByLabelText(/auto-apply/i)).toBeEnabled();
  });

  it('leaves both open to an admin', () => {
    setOrgRole('admin');
    renderWithClient(<WorkspaceSettings workspace={workspace({ requires_approval: true })} />);

    expect(screen.getByLabelText(/require approval/i)).toBeEnabled();
    expect(screen.getByLabelText(/auto-apply/i)).toBeEnabled();
  });
});
