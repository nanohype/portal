import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import { AccountCreateModal } from './AccountCreateModal';

// Mock the api client at the module boundary (openapi-fetch binds fetch at
// import, so network-level interception is brittle).
const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock('@/api/client', () => ({ api: apiMock }));

const VEND_ROLE = 'arn:aws:iam::111111111111:role/production-fleet-vend';
const CLUSTER_PB = 'arn:aws:iam::111111111111:policy/production-cluster-boundary';
const OPERATOR_PB = 'arn:aws:iam::111111111111:policy/production-operator-boundary';

async function fillRequired(user: UserEvent) {
  await user.type(screen.getByPlaceholderText('e.g. production'), 'production');
  await user.type(screen.getByPlaceholderText('123456789012'), '111111111111');
  await user.type(
    screen.getByPlaceholderText('arn:aws:iam::123456789012:role/portal-cross-account'),
    'arn:aws:iam::111111111111:role/portal-cross-account',
  );
  await user.type(screen.getByPlaceholderText('us-west-2'), 'us-west-2');
}

async function openSubstrate(user: UserEvent) {
  await user.click(screen.getByRole('button', { name: /substrate prerequisites/i }));
}

function submitButton() {
  return screen.getByRole('button', { name: /create account/i });
}

describe('AccountCreateModal — substrate prerequisites', () => {
  // Without this, mock.calls[0] is whichever test ran first.
  beforeEach(() => {
    apiMock.POST.mockReset();
  });

  it('submits without any of them: same-account vending needs none', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'a1' }, error: undefined });
    renderWithClient(<AccountCreateModal open onClose={() => {}} />);

    await fillRequired(user);
    await user.click(submitButton());

    expect(apiMock.POST).toHaveBeenCalledTimes(1);
    const body = apiMock.POST.mock.calls[0][1].body;
    // Absent rather than empty-string, so the column stays NULL and "unset"
    // reads the same as an account registered before these existed.
    expect(body.vend_role_arn).toBeUndefined();
    expect(body.operator_permissions_boundary_arn).toBeUndefined();
  });

  // The rule the order path enforces too. Blocking here is the whole reason the
  // client mirrors it: otherwise the account looks registered and the first
  // cross-account vend is the thing that fails.
  it('refuses a fleet-vend role with neither permissions boundary', async () => {
    const user = userEvent.setup();
    renderWithClient(<AccountCreateModal open onClose={() => {}} />);

    await fillRequired(user);
    await openSubstrate(user);
    await user.type(
      screen.getByPlaceholderText('arn:aws:iam::123456789012:role/production-fleet-vend'),
      VEND_ROLE,
    );

    expect(screen.getByText(/needs both permissions boundaries/i)).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();
  });

  it('still refuses when only one boundary is supplied', async () => {
    const user = userEvent.setup();
    renderWithClient(<AccountCreateModal open onClose={() => {}} />);

    await fillRequired(user);
    await openSubstrate(user);
    await user.type(
      screen.getByPlaceholderText('arn:aws:iam::123456789012:role/production-fleet-vend'),
      VEND_ROLE,
    );
    await user.type(
      screen.getByPlaceholderText('arn:aws:iam::123456789012:policy/production-cluster-boundary'),
      CLUSTER_PB,
    );

    expect(submitButton()).toBeDisabled();
  });

  it('accepts the complete cross-account set and sends all three', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'a1' }, error: undefined });
    renderWithClient(<AccountCreateModal open onClose={() => {}} />);

    await fillRequired(user);
    await openSubstrate(user);
    await user.type(
      screen.getByPlaceholderText('arn:aws:iam::123456789012:role/production-fleet-vend'),
      VEND_ROLE,
    );
    await user.type(
      screen.getByPlaceholderText('arn:aws:iam::123456789012:policy/production-cluster-boundary'),
      CLUSTER_PB,
    );
    await user.type(
      screen.getByPlaceholderText('arn:aws:iam::123456789012:policy/production-operator-boundary'),
      OPERATOR_PB,
    );

    expect(screen.queryByText(/needs both permissions boundaries/i)).not.toBeInTheDocument();
    await user.click(submitButton());

    const body = apiMock.POST.mock.calls[0][1].body;
    expect(body.vend_role_arn).toBe(VEND_ROLE);
    expect(body.cluster_permissions_boundary_arn).toBe(CLUSTER_PB);
    expect(body.operator_permissions_boundary_arn).toBe(OPERATOR_PB);
  });

  // A KMS key is not gated by the role rule — an account that vends
  // same-account may still have one.
  it('accepts a data KMS key on its own', async () => {
    const user = userEvent.setup();
    apiMock.POST.mockResolvedValue({ data: { id: 'a1' }, error: undefined });
    renderWithClient(<AccountCreateModal open onClose={() => {}} />);

    await fillRequired(user);
    await openSubstrate(user);
    await user.type(
      screen.getByPlaceholderText('arn:aws:kms:us-west-2:123456789012:key/abcd-1234'),
      'arn:aws:kms:us-west-2:111111111111:key/abcd-1234',
    );

    expect(submitButton()).toBeEnabled();
    await user.click(submitButton());
    expect(apiMock.POST.mock.calls[0][1].body.data_kms_key_arn).toBe(
      'arn:aws:kms:us-west-2:111111111111:key/abcd-1234',
    );
  });

  it('rejects an ARN that is not one', async () => {
    const user = userEvent.setup();
    renderWithClient(<AccountCreateModal open onClose={() => {}} />);

    await fillRequired(user);
    await openSubstrate(user);
    await user.type(
      screen.getByPlaceholderText('arn:aws:kms:us-west-2:123456789012:key/abcd-1234'),
      'my-kms-key',
    );

    expect(screen.getByText(/arn:aws:<iam\|kms>/i)).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();
  });
});
