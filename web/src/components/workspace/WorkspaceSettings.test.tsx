import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User, Workspace } from '@/api/models';
import { WorkspaceSettings } from './WorkspaceSettings';

const { apiMock, toastMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
  toastMock: { success: vi.fn(), error: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));
vi.mock('sonner', () => ({ toast: toastMock }));
vi.mock('@/hooks/useNavigate', () => ({ navigate: vi.fn() }));

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

// Deleting the last workspace requiring approval on a repo + working_dir leaves
// that configuration open to an ungated one, so the server holds it at the same
// bar as clearing the gate and answers with the way out. A refusal the UI
// rewrites into "Failed to delete workspace" is a dead button with no reason on
// it, so the message has to come through as the server wrote it.
describe('WorkspaceSettings delete', () => {
  function confirmAndDelete(name: string) {
    fireEvent.change(screen.getByLabelText(/to confirm/i), { target: { value: name } });
    fireEvent.click(screen.getByRole('button', { name: /delete workspace/i }));
  }

  it('surfaces the refusal the server gave', async () => {
    setOrgRole('operator');
    const refusal =
      'this is the only workspace requiring approval for its repository and working directory, ' +
      'so deleting it would leave that configuration ungated: leave another workspace requiring ' +
      'approval on it, or hold admin role or higher';
    apiMock.DELETE.mockResolvedValue({ data: undefined, error: { message: refusal } });

    renderWithClient(<WorkspaceSettings workspace={workspace({ requires_approval: true })} />);
    confirmAndDelete('dev-vpc');

    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith(refusal));
  });

  it('still deletes a workspace the server allows', async () => {
    setOrgRole('operator');
    apiMock.DELETE.mockResolvedValue({ data: undefined, error: undefined });

    renderWithClient(<WorkspaceSettings workspace={workspace()} />);
    confirmAndDelete('dev-vpc');

    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Workspace deleted'));
    expect(toastMock.error).not.toHaveBeenCalled();
  });
});
