import { describe, it, expect } from 'vitest';
import {
  DATASTORE_KINDS,
  KIND_IMPLEMENTATIONS,
  datastoreError,
  emptyDatastore,
  fromDatastores,
  isDatastoreKind,
  nameBudget,
  toDatastores,
  type DatastoreDraft,
} from './datastores';

function draft(over: Partial<DatastoreDraft> = {}): DatastoreDraft {
  return { ...emptyDatastore(), name: 'docs', ...over };
}

describe('nameBudget', () => {
  // ElastiCache caps a replication-group id at 40 characters including the
  // longest environment token, one tighter than the CRD's own 28-char rule — so a
  // cache at exactly 28 passes admission and then fails the tofu module.
  it('is one character tighter for a cache', () => {
    expect(nameBudget('cache')).toBe(27);
    for (const kind of DATASTORE_KINDS.filter((k) => k !== 'cache')) {
      expect(nameBudget(kind)).toBe(28);
    }
  });
});

describe('datastoreError', () => {
  it('accepts a legal row', () => {
    expect(datastoreError(draft(), 'acme', [draft()])).toBeNull();
  });

  it('requires a name', () => {
    expect(datastoreError(draft({ name: '' }), 'acme', [])).toMatch(/required/i);
  });

  it('rejects a name outside the RFC-1123 label shape', () => {
    for (const name of ['Docs', 'my_docs', '-docs', 'docs-', 'nineteen-chars-abcd']) {
      expect(datastoreError(draft({ name }), 'acme', [])).toMatch(/alphanumeric/);
    }
  });

  it('rejects duplicate names', () => {
    const rows = [draft(), draft()];
    expect(datastoreError(rows[0], 'acme', rows)).toMatch(/unique/);
  });

  it('enforces the composed-name budget, and the tighter cache one', () => {
    // 21 + 7 = 28: legal for every kind except cache.
    const platform = 'twenty-two-chars-here';
    expect(datastoreError(draft({ name: 'hotstor' }), platform, [])).toBeNull();
    expect(datastoreError(draft({ name: 'hotcach', kind: 'cache' }), platform, [])).toMatch(
      /27-char budget for cache/,
    );
  });

  // The budget depends on the tenant name, which the operator types after adding
  // rows — so a row that was legal has to be able to become illegal.
  it('skips the budget while the platform name is empty', () => {
    expect(datastoreError(draft({ name: 'ledger' }), '', [])).toBeNull();
  });

  // A DynamoDB table has no default partition key, so the CRD requires one.
  it('requires a partition key on a keyValue store', () => {
    expect(datastoreError(draft({ kind: 'keyValue' }), 'acme', [])).toMatch(/partition key/);
    expect(
      datastoreError(draft({ kind: 'keyValue', partitionKeyName: 'docId' }), 'acme', []),
    ).toBeNull();
  });
});

describe('toDatastores', () => {
  it('emits the keyValue block only for that kind', () => {
    const [store, table] = toDatastores([
      draft({ name: 'corpus', kind: 'objectStore' }),
      draft({ name: 'chunks', kind: 'keyValue', partitionKeyName: 'docId', partitionKeyType: 'N' }),
    ]);
    expect(store).toEqual({ name: 'corpus', kind: 'objectStore', deletionPolicy: 'Retain' });
    expect(table).toEqual({
      name: 'chunks',
      kind: 'keyValue',
      deletionPolicy: 'Retain',
      keyValue: { partitionKey: { name: 'docId', type: 'N' } },
    });
  });

  it('carries an explicit Delete policy through', () => {
    expect(toDatastores([draft({ deletionPolicy: 'Delete' })])[0].deletionPolicy).toBe('Delete');
  });
});

describe('fromDatastores', () => {
  it('round-trips what toDatastores emits', () => {
    const drafts = [
      draft({ name: 'corpus', kind: 'objectStore', deletionPolicy: 'Delete' }),
      draft({ name: 'chunks', kind: 'keyValue', partitionKeyName: 'docId', partitionKeyType: 'B' }),
    ];
    expect(fromDatastores(toDatastores(drafts))).toEqual(drafts);
  });

  it('drops entries it cannot read rather than surfacing a half-parsed row', () => {
    expect(
      fromDatastores([
        'not-an-object',
        null,
        { name: 'docs' }, // no kind
        { name: 'docs', kind: 'documentDb' }, // not in the vocabulary
        { name: 'good', kind: 'queue' },
      ]),
    ).toEqual([draft({ name: 'good', kind: 'queue' })]);
  });

  it('is empty for anything that is not a list', () => {
    for (const raw of [undefined, null, {}, 'docs', 7]) {
      expect(fromDatastores(raw)).toEqual([]);
    }
  });

  // An unquoted `type: N` is a YAML 1.1 boolean, so a hand-edited template's
  // default_values can carry `false` where a type belongs. Falling back to S
  // beats rendering `type: false` into a Platform the API server rejects.
  it('falls back to S for an unreadable attribute type', () => {
    const [row] = fromDatastores([
      {
        name: 'chunks',
        kind: 'keyValue',
        keyValue: { partitionKey: { name: 'docId', type: false } },
      },
    ]);
    expect(row.partitionKeyType).toBe('S');
  });
});

describe('the vocabulary', () => {
  it('names an implementation for every kind', () => {
    for (const kind of DATASTORE_KINDS) {
      expect(KIND_IMPLEMENTATIONS[kind]).toBeTruthy();
    }
  });

  it('recognizes exactly the kinds it lists', () => {
    for (const kind of DATASTORE_KINDS) {
      expect(isDatastoreKind(kind)).toBe(true);
    }
    expect(isDatastoreKind('documentDb')).toBe(false);
  });
});
