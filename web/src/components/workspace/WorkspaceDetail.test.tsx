import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User, Workspace } from '@/api/models';
import { WorkspaceDetail } from './WorkspaceDetail';

// The api client binds globalThis.fetch at import, so mock it at the module
// boundary. navigate() reaches the real router ref and confirm() comes from a
// provider we don't mount here.
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

// The caller's role on THIS workspace: their org role, raised by any grant one
// of their teams holds here. The API computes it (handler/workspace.go) and
// authorizes import/destroy/run against it, so the page has to read it too.
function workspace(overrides: Partial<Workspace> = {}) {
  return {
    id: 'ws-1',
    name: 'app-competitive-intelligence',
    description: '',
    environment: 'production',
    source: 'vcs',
    repo_url: 'https://example.test/infra.git',
    repo_branch: 'main',
    working_dir: 'envs/prod',
    tofu_version: '1.9.0',
    locked: false,
    auto_apply: false,
    requires_approval: false,
    effective_role: 'operator',
    updated_at: new Date().toISOString(),
    ...overrides,
  } as unknown as Workspace;
}

function setOrgRole(role: string) {
  useAuthStore.setState({
    token: 'test-token',
    user: { id: 'u1', role } as User,
    isAuthenticated: true,
  });
}

function mockApi(ws: Workspace) {
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/auth/me')
      return Promise.resolve({ data: useAuthStore.getState().user, error: undefined });
    if (path === '/workspaces/{workspaceId}')
      return Promise.resolve({ data: ws, error: undefined });
    if (path === '/workspaces/{workspaceId}/runs')
      return Promise.resolve({ data: { data: [], total: 0, page: 1, per_page: 20 } });
    return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
  });
  apiMock.POST.mockResolvedValue({ data: { id: 'run-1' }, error: undefined });
}

beforeEach(() => {
  vi.clearAllMocks();
  confirmMock.mockResolvedValue(true);
});

// rbac.go puts ActionApplyRun at operator, and handler/run.go only raises the
// bar on a workspace that requires approval. An operator who can start a plan
// has to be able to finish it.
describe('WorkspaceDetail apply', () => {
  it('gives an operator an apply on an ungated workspace', async () => {
    setOrgRole('operator');
    mockApi(workspace());
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    const apply = await screen.findByRole('button', { name: /^apply$/i });
    expect(apply).toBeEnabled();

    await userEvent.click(apply);
    expect(confirmMock).toHaveBeenCalledOnce();
    await waitFor(() => expect(apiMock.POST).toHaveBeenCalledOnce());
    const [path, opts] = apiMock.POST.mock.calls[0];
    expect(path).toBe('/workspaces/{workspaceId}/runs');
    expect(opts.body).toEqual({ operation: 'apply' });
  });

  it('does not apply when the confirm is dismissed', async () => {
    setOrgRole('operator');
    confirmMock.mockResolvedValue(false);
    mockApi(workspace());
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    await userEvent.click(await screen.findByRole('button', { name: /^apply$/i }));
    expect(apiMock.POST).not.toHaveBeenCalled();
  });

  it('offers no apply to a viewer — the API refuses it', async () => {
    setOrgRole('viewer');
    mockApi(workspace({ effective_role: 'viewer' }));
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    await screen.findByRole('heading', { name: /app-competitive-intelligence/i });
    expect(screen.queryByRole('button', { name: /^apply$/i })).toBeNull();
  });

  // On a gated workspace the apply goes through an approval an admin signs, so
  // the direct control is closed and says why.
  it('holds apply on a workspace that requires approval', async () => {
    setOrgRole('operator');
    mockApi(workspace({ requires_approval: true }));
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    expect(await screen.findByRole('button', { name: /^apply$/i })).toBeDisabled();
  });

  it('leaves apply open on a gated workspace for an org admin', async () => {
    setOrgRole('admin');
    mockApi(workspace({ requires_approval: true, effective_role: 'admin' }));
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    expect(await screen.findByRole('button', { name: /^apply$/i })).toBeEnabled();
  });
});

// Import and destroy are authorized against the workspace-effective role
// (handler/run.go reads auth.WorkspaceRole), so a team grant that elevates on
// this one workspace has to open the controls too.
describe('WorkspaceDetail state actions follow the workspace-effective role', () => {
  it('enables import and destroy for an org operator holding an admin grant here', async () => {
    setOrgRole('operator');
    mockApi(workspace({ effective_role: 'admin' }));
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    expect(await screen.findByRole('button', { name: /^import$/i })).toBeEnabled();
    expect(screen.getByRole('button', { name: /^destroy$/i })).toBeEnabled();
  });

  it('keeps import and destroy closed for an operator with no grant', async () => {
    setOrgRole('operator');
    mockApi(workspace({ effective_role: 'operator' }));
    renderWithClient(<WorkspaceDetail workspaceId="ws-1" />);

    expect(await screen.findByRole('button', { name: /^import$/i })).toBeDisabled();
    expect(screen.getByRole('button', { name: /^destroy$/i })).toBeDisabled();
  });
});
