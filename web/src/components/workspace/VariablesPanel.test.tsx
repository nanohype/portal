import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { VariablesPanel } from './VariablesPanel';

const { apiMock, confirmMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
  confirmMock: vi.fn(),
}));
vi.mock('@/api/client', () => ({ api: apiMock }));
vi.mock('@/components/ui/confirm-context', () => ({
  useConfirm: () => confirmMock,
}));

// What /discover answers below the variable-management bar: the shape of the
// config, with the value column gone (service/discovery.go withoutValues).
const REDACTED_ROWS = [
  { name: 'cluster_name', type: 'string', configured: true, configured_by: 'terragrunt' },
  { name: 'db_password', type: 'string', configured: true, configured_by: 'terragrunt' },
];

// What it answers at or above that bar.
const VALUED_ROWS = [
  {
    name: 'cluster_name',
    type: 'string',
    default: '"prod"',
    configured: true,
    configured_by: 'terragrunt',
  },
  { name: 'replicas', type: 'number', default: '3', configured: false },
];

function mockApi(discoverRows: unknown[]) {
  apiMock.GET.mockImplementation(() =>
    Promise.resolve({ data: { data: [], total: 0, page: 1, per_page: 50 } }),
  );
  apiMock.POST.mockImplementation((path: string) => {
    if (path === '/workspaces/{workspaceId}/variables/discover')
      return Promise.resolve({ data: discoverRows, error: undefined });
    return Promise.resolve({ error: { message: `unexpected POST ${path}` } });
  });
}

beforeEach(() => {
  vi.clearAllMocks();
});

// Discovery is a read of the config's shape, so it stays reachable at the read
// bar — but everything it feeds is a write, and the API answers a below-the-bar
// caller with no values at all.
describe('VariablesPanel discovery', () => {
  it('shows a viewer the names it found, with no values and no way to add them', async () => {
    mockApi(REDACTED_ROWS);
    renderWithClient(<VariablesPanel workspaceId="ws-1" role="viewer" />);

    await userEvent.click(await screen.findByRole('button', { name: /discover/i }));

    expect(await screen.findByText('cluster_name')).toBeInTheDocument();
    expect(screen.getByText('db_password')).toBeInTheDocument();
    // No `=value` anywhere, and none of the write controls.
    expect(screen.queryByText(/^=/)).toBeNull();
    expect(screen.queryByRole('button', { name: /^add all/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /^add$/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /add variable/i })).toBeNull();
  });

  it('gives an admin the values and the controls that act on them', async () => {
    mockApi(VALUED_ROWS);
    renderWithClient(<VariablesPanel workspaceId="ws-1" role="admin" />);

    await userEvent.click(await screen.findByRole('button', { name: /discover/i }));

    expect(await screen.findByText('="prod"')).toBeInTheDocument();
    expect(screen.getByText('=3')).toBeInTheDocument();
    // replicas is the unconfigured one, so it is the one Add-all would create.
    expect(screen.getByRole('button', { name: /^add all/i })).toBeInTheDocument();
  });
});
