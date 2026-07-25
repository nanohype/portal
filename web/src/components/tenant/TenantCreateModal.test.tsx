import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { useAuthStore } from '@/stores/auth';
import type { Cluster, User } from '@/api/models';
import { TenantCreateModal } from './TenantCreateModal';

// Mock the api client at the module boundary (openapi-fetch binds fetch at
// import, so network-level interception is brittle).
const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

const TEMPLATE = {
  id: 'tpl1',
  name: 'starter',
  description: '',
  persona: 'eng',
  max_budget_usd: 1000,
  allowed_overrides: ['budget.monthlyUsd', 'platform.persona'],
  allowed_model_families: [],
  allowed_datastore_kinds: [],
  required_compliance: [],
  default_values: { budget: { monthlyUsd: 500 }, platform: { persona: 'eng' } },
};

const CLUSTER = {
  id: 'cl1',
  name: 'prod-eks',
  region: 'us-west-2',
  connection_status: 'connected',
} as unknown as Cluster;

function list<T>(items: T[]) {
  return { data: { data: items, total: items.length, page: 1, per_page: 50 } };
}

// Open the custom Select and click the named option. The trigger is a
// role="combobox" button whose accessible name doesn't come from its text, so
// target it by the displayed value text and walk up to the combobox. findBy so
// we wait out async-loaded Selects (Template renders after its query resolves).
async function choose(user: UserEvent, current: RegExp, option: RegExp) {
  const trigger = (await screen.findByText(current)).closest('[role="combobox"]');
  if (!trigger) throw new Error(`no combobox showing ${current}`);
  await user.click(trigger);
  await user.click(await screen.findByRole('option', { name: option }));
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.setState({
    token: 'test-token',
    user: { id: 'u1', role: 'admin' } as User,
    isAuthenticated: true,
  });
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/templates') return Promise.resolve(list([TEMPLATE]));
    if (path === '/teams') return Promise.resolve(list([]));
    return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
  });
});

describe('TenantCreateModal canSubmit gating', () => {
  it('enables Create only once a cluster, template, and valid name are set', async () => {
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);

    const create = await screen.findByRole('button', { name: /create tenant/i });
    expect(create).toBeDisabled();

    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /starter/i);
    await user.type(screen.getByPlaceholderText('marketing-team'), 'eng-team');

    await waitFor(() => expect(create).toBeEnabled());
  });

  it('keeps Create disabled for an invalid k8s name', async () => {
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);

    await screen.findByRole('button', { name: /create tenant/i });
    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /starter/i);
    await user.type(screen.getByPlaceholderText('marketing-team'), 'Bad_Name');

    expect(screen.getByRole('button', { name: /create tenant/i })).toBeDisabled();
  });

  it('keeps Create disabled when the budget exceeds the template cap', async () => {
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);

    await screen.findByRole('button', { name: /create tenant/i });
    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /starter/i);
    await user.type(screen.getByPlaceholderText('marketing-team'), 'eng-team');

    // Over the template's $1000 cap.
    const budget = screen.getByRole('spinbutton');
    await user.clear(budget);
    await user.type(budget, '2000');

    await waitFor(() =>
      expect(screen.getByRole('button', { name: /create tenant/i })).toBeDisabled(),
    );
  });
});

// A template that allowlists the vocabulary paths, so the fields are editable.
const VOCAB_TEMPLATE = {
  ...TEMPLATE,
  id: 'tpl2',
  name: 'with-substrate',
  allowed_overrides: [
    'budget.monthlyUsd',
    'datastores',
    'identity.capabilities',
    'identity.directSecretReads',
    'attribution.operators',
  ],
};

describe('TenantCreateModal vocabulary', () => {
  beforeEach(() => {
    apiMock.GET.mockImplementation((path: string) => {
      if (path === '/templates') return Promise.resolve(list([VOCAB_TEMPLATE]));
      if (path === '/teams') return Promise.resolve(list([]));
      return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
    });
    apiMock.POST.mockResolvedValue({ data: { id: 'op1' } });
  });

  async function fillBasics(user: UserEvent, name = 'eng-team') {
    await screen.findByRole('button', { name: /create tenant/i });
    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /with-substrate/i);
    await user.type(screen.getByPlaceholderText('marketing-team'), name);
  }

  it('sends the declared datastores, capabilities, secret reads and operators', async () => {
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);
    await fillBasics(user);

    await user.click(screen.getByRole('button', { name: /add datastore/i }));
    await user.type(screen.getByLabelText(/datastore 1 name/i), 'work');
    await user.click(screen.getByLabelText(/datastore 1 kind/i));
    await user.click(await screen.findByRole('option', { name: /^queue$/ }));

    await user.click(screen.getByRole('checkbox', { name: /eventBridgeScheduler/i }));
    await user.type(screen.getByLabelText(/direct secret reads/i), 'vendor/api-token');
    await user.type(screen.getByLabelText(/attribution operators/i), 'operator@example.com');

    await user.click(screen.getByRole('button', { name: /create tenant/i }));

    await waitFor(() => expect(apiMock.POST).toHaveBeenCalled());
    const values = apiMock.POST.mock.calls[0][1].body.values;
    expect(values.datastores).toEqual([{ name: 'work', kind: 'queue', deletionPolicy: 'Retain' }]);
    expect(values.identity.capabilities).toEqual(['eventBridgeScheduler']);
    expect(values.identity.directSecretReads).toEqual(['vendor/api-token']);
    expect(values.attribution.operators).toEqual(['operator@example.com']);
  });

  // The operator scopes the minted scheduler-invoke role's SendMessage to the
  // tenant's own queues, so the capability with no queue is a role carrying no
  // grant. The chart rejects it at render; the form should not get that far.
  it('blocks submit when eventBridgeScheduler has no queue to send to', async () => {
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);
    await fillBasics(user);

    await user.click(screen.getByRole('checkbox', { name: /eventBridgeScheduler/i }));

    await waitFor(() =>
      expect(screen.getByRole('button', { name: /create tenant/i })).toBeDisabled(),
    );
    // The capability's own hint also mentions a queue, so match the error's tail.
    expect(screen.getByText(/minted role carries no grant/i)).toBeInTheDocument();
  });

  // The budget depends on the tenant name, so a row that was legal when added
  // becomes illegal as a longer name is typed.
  it('blocks submit when the tenant name pushes a datastore over its budget', async () => {
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);
    await fillBasics(user, 'a-tenant-name-that-is-quite-long');

    await user.click(screen.getByRole('button', { name: /add datastore/i }));
    await user.type(screen.getByLabelText(/datastore 1 name/i), 'ledger');

    await waitFor(() =>
      expect(screen.getByRole('button', { name: /create tenant/i })).toBeDisabled(),
    );
    expect(screen.getByText(/over the 28-char budget/i)).toBeInTheDocument();
  });

  // A locked path must not reach the payload even though the form knows the value:
  // buildOverrides drops it, and the field is disabled so it cannot be edited.
  it('omits a vocabulary path the template does not allow', async () => {
    apiMock.GET.mockImplementation((path: string) => {
      if (path === '/templates')
        return Promise.resolve(list([{ ...VOCAB_TEMPLATE, allowed_overrides: ['datastores'] }]));
      if (path === '/teams') return Promise.resolve(list([]));
      return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
    });
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);
    await fillBasics(user);

    expect(screen.getByLabelText(/direct secret reads/i)).toBeDisabled();

    await user.click(screen.getByRole('button', { name: /create tenant/i }));
    await waitFor(() => expect(apiMock.POST).toHaveBeenCalled());
    const values = apiMock.POST.mock.calls[0][1].body.values;
    expect(values.identity).toBeUndefined();
    expect(values.attribution).toBeUndefined();
  });

  // The template cap narrows the picker itself, so an operator cannot select a
  // kind the server would reject.
  it('offers only the kinds the template allows', async () => {
    apiMock.GET.mockImplementation((path: string) => {
      if (path === '/templates')
        return Promise.resolve(
          list([{ ...VOCAB_TEMPLATE, allowed_datastore_kinds: ['objectStore', 'queue'] }]),
        );
      if (path === '/teams') return Promise.resolve(list([]));
      return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
    });
    const user = userEvent.setup();
    renderWithClient(<TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />);
    await fillBasics(user);

    await user.click(screen.getByRole('button', { name: /add datastore/i }));
    await user.click(screen.getByLabelText(/datastore 1 kind/i));

    expect(await screen.findByRole('option', { name: /^queue$/ })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: /^relational$/ })).not.toBeInTheDocument();
  });
});
