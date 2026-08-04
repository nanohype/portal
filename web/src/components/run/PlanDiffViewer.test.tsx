import { describe, it, expect } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithClient } from '@/test/render';
import type { TofuPlanJSON, TofuResourceChange } from '@/api/models';
import { PlanDiffViewer } from './PlanDiffViewer';

const REDACTED = '(sensitive value)';

function change(
  address: string,
  actions: string[],
  before: Record<string, unknown> | null,
  after: Record<string, unknown> | null,
): TofuResourceChange {
  return {
    address,
    mode: 'managed',
    type: address.split('.')[0],
    name: address.split('.')[1],
    provider_name: 'registry.opentofu.org/hashicorp/aws',
    change: { actions, before, after },
  };
}

function plan(changes: TofuResourceChange[], valuesRedacted = false): TofuPlanJSON {
  return {
    format_version: '1.2',
    resource_changes: changes,
    ...(valuesRedacted ? { sensitive_values_redacted: true } : {}),
  };
}

// THE BUG THIS EXISTS TO CATCH: the API withholds values the plan marks
// sensitive, so both sides of a rotated secret arrive reading "(sensitive
// value)". This component used to work out which attributes changed by comparing
// before against after — which, for that resource, finds nothing. The run would
// render "terraform_data.db will be updated", with no attributes under it and no
// indication anything had been withheld. A password rotation would look like a
// no-op to whoever was reviewing it.
//
// The API sends only the attributes that differ, so the keys ARE the answer.
describe('PlanDiffViewer with values withheld', () => {
  it('still reports an attribute whose value is redacted on both sides', async () => {
    const user = userEvent.setup();
    renderWithClient(
      <PlanDiffViewer
        planOutput=""
        planJSON={plan(
          [
            change(
              'aws_db_instance.main',
              ['update'],
              { password: REDACTED },
              { password: REDACTED },
            ),
          ],
          true,
        )}
      />,
    );

    expect(screen.getByText('aws_db_instance.main')).toBeInTheDocument();
    expect(screen.getByText(/1 attribute\(s\)/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /aws_db_instance\.main/ }));
    expect(screen.getByText(/password/)).toBeInTheDocument();
  });

  it('says values were withheld, so an empty-looking diff is never mistaken for the whole story', () => {
    renderWithClient(
      <PlanDiffViewer
        planOutput=""
        planJSON={plan(
          [change('aws_db_instance.main', ['update'], { p: REDACTED }, { p: REDACTED })],
          true,
        )}
      />,
    );

    expect(screen.getByText(/sensitive values withheld/i)).toBeInTheDocument();
  });

  it('says nothing about withholding to a caller who was served the values', () => {
    renderWithClient(
      <PlanDiffViewer
        planOutput=""
        planJSON={plan([change('aws_db_instance.main', ['update'], { p: 'old' }, { p: 'new' })])}
      />,
    );

    expect(screen.queryByText(/sensitive values withheld/i)).not.toBeInTheDocument();
  });
});

describe('PlanDiffViewer attribute classification', () => {
  it('reads a null before as a create and a null after as a destroy', () => {
    renderWithClient(
      <PlanDiffViewer
        planOutput=""
        planJSON={plan([
          change('aws_s3_bucket.new', ['create'], null, { bucket: 'made' }),
          change('aws_s3_bucket.old', ['delete'], { bucket: 'gone' }, null),
        ])}
      />,
    );

    expect(screen.getByText('+1 to add')).toBeInTheDocument();
    expect(screen.getByText('-1 to destroy')).toBeInTheDocument();
    expect(screen.getAllByText(/1 attribute\(s\)/)).toHaveLength(2);
  });

  it('counts every attribute it is sent, because the API sends only the ones that differ', () => {
    renderWithClient(
      <PlanDiffViewer
        planOutput=""
        planJSON={plan([
          change(
            'aws_instance.web',
            ['update'],
            { tags: { env: 'dev' }, user_data: REDACTED },
            { tags: { env: 'prod' }, user_data: REDACTED },
          ),
        ])}
      />,
    );

    expect(screen.getByText(/2 attribute\(s\)/)).toBeInTheDocument();
  });
});

describe('PlanDiffViewer without a structured plan', () => {
  it('falls back to the rendered plan text', () => {
    renderWithClient(
      <PlanDiffViewer
        planOutput={
          '# aws_s3_bucket.b will be created\n  + resource "aws_s3_bucket" "b" {\n      + bucket = "x"\n    }\n\nPlan: 1 to add, 0 to change, 0 to destroy.'
        }
        planJSON={null}
      />,
    );

    expect(screen.getByText(/aws_s3_bucket\.b will be created/)).toBeInTheDocument();
    expect(screen.getByText(/Plan: 1 to add/)).toBeInTheDocument();
  });

  it('falls back when the plan touches nothing', () => {
    renderWithClient(
      <PlanDiffViewer
        planOutput={'No changes. Your infrastructure matches the configuration.'}
        planJSON={plan([])}
      />,
    );

    expect(screen.getByText(/No changes/)).toBeInTheDocument();
  });
});
