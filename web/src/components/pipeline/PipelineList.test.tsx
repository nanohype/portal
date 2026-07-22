import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { User } from '@/api/models';
import { ConfirmProvider } from '@/components/ui/confirm';
import { PipelineList } from './PipelineList';

const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

const WORKSPACES = [
  { id: 'ws-dev', name: 'dev-vpc', requires_approval: false },
  { id: 'ws-prod', name: 'prod-vpc', requires_approval: true },
];

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
    if (path === '/workspaces')
      return Promise.resolve({ data: { data: WORKSPACES }, error: undefined });
    return Promise.resolve({ data: { data: [] }, error: undefined });
  });
});

// Open the create dialog and add one workspace stage through the custom select.
async function addStage(user: UserEvent, workspaceName: RegExp) {
  await user.click(await screen.findByRole('button', { name: /new pipeline/i }));
  const trigger = (await screen.findByText(/add workspace stage/i)).closest('[role="combobox"]');
  if (!trigger) throw new Error('no workspace stage select');
  await user.click(trigger);
  await user.click(await screen.findByRole('option', { name: workspaceName }));
}

// A stage's auto_apply is worth what the workspace it targets is worth. On an
// ungated workspace it is the apply the operator may already start by hand, so
// the dialog offers it; on a workspace that requires approval it is the admin
// bar, and the stage would park for a signature regardless.
describe('CreatePipelineDialog stage auto-apply', () => {
  it('defaults an operator stage on an ungated workspace to auto', async () => {
    setOrgRole('operator');
    const user = userEvent.setup();
    renderWithClient(
      <ConfirmProvider>
        <PipelineList />
      </ConfirmProvider>,
    );

    await addStage(user, /dev-vpc/i);
    const toggle = screen.getByTitle(/toggle whether this stage applies automatically/i);
    expect(toggle).toBeEnabled();
    expect(toggle).toHaveTextContent(/auto/i);
  });

  it('leaves an operator stage on a gated workspace manual and locked', async () => {
    setOrgRole('operator');
    const user = userEvent.setup();
    renderWithClient(
      <ConfirmProvider>
        <PipelineList />
      </ConfirmProvider>,
    );

    await addStage(user, /prod-vpc/i);
    const toggle = screen.getByTitle(/this workspace requires approval/i);
    expect(toggle).toBeDisabled();
    expect(toggle).toHaveTextContent(/manual/i);
    await screen.findByText(/pauses for an admin to sign/i);
  });

  it('lets an admin set auto-apply on a gated workspace', async () => {
    setOrgRole('admin');
    const user = userEvent.setup();
    renderWithClient(
      <ConfirmProvider>
        <PipelineList />
      </ConfirmProvider>,
    );

    await addStage(user, /prod-vpc/i);
    const toggle = screen.getByTitle(/toggle whether this stage applies automatically/i);
    expect(toggle).toBeEnabled();
    expect(toggle).toHaveTextContent(/auto/i);
  });
});
