// The Platform CRD's datastore vocabulary, shared by the tenant create form
// (which declares datastores) and the template editor (which caps which kinds may
// be declared). One list, so the picker and the cap can never offer different
// kinds.
//
// DatastoreKind is derived from the generated API contract rather than written
// out again, so adding a kind to api/openapi.yaml is what widens this — not a
// second edit here that could be forgotten.

import type { Template } from '@/api/models';

export type DatastoreKind = NonNullable<Template['allowed_datastore_kinds']>[number];

export const DATASTORE_KINDS = [
  'relational',
  'keyValue',
  'objectStore',
  'queue',
  'cache',
  'stream',
] as const satisfies readonly DatastoreKind[];

// What each kind maps to, shown next to the picker so an operator declaring
// "relational" knows they are asking for an Aurora cluster.
export const KIND_IMPLEMENTATIONS: Record<DatastoreKind, string> = {
  relational: 'Aurora PostgreSQL Serverless v2',
  keyValue: 'DynamoDB',
  objectStore: 'S3',
  queue: 'SQS',
  cache: 'ElastiCache (Valkey)',
  stream: 'MSK Serverless',
};

export const ATTRIBUTE_TYPES = ['S', 'N', 'B'] as const;
export type AttributeType = (typeof ATTRIBUTE_TYPES)[number];

const NAME_RE = /^[a-z0-9]([a-z0-9-]{0,16}[a-z0-9])?$/;

// The platform name and the datastore name compose into the provisioned bucket /
// table / queue name. A cache is one character tighter than the rest: ElastiCache
// caps a replication-group id at 40 including the longest environment token, so a
// cache at exactly 28 passes admission and then fails the tofu module.
export function nameBudget(kind: DatastoreKind): number {
  return kind === 'cache' ? 27 : 28;
}

export function isDatastoreKind(k: string): k is DatastoreKind {
  return (DATASTORE_KINDS as readonly string[]).includes(k);
}

// The form's working shape. Flat, because a row's inputs are flat; toDatastores
// folds it into the nested Platform shape on submit.
export interface DatastoreDraft {
  name: string;
  kind: DatastoreKind;
  deletionPolicy: 'Retain' | 'Delete';
  partitionKeyName: string;
  partitionKeyType: AttributeType;
}

export function emptyDatastore(kind: DatastoreKind = 'objectStore'): DatastoreDraft {
  return {
    name: '',
    kind,
    deletionPolicy: 'Retain',
    partitionKeyName: '',
    partitionKeyType: 'S',
  };
}

/** The first problem with a row, or null when it is legal. */
export function datastoreError(
  draft: DatastoreDraft,
  platformName: string,
  siblings: DatastoreDraft[],
): string | null {
  if (!draft.name) return 'Name is required';
  if (!NAME_RE.test(draft.name)) {
    return 'Lowercase alphanumeric + hyphen, at most 18 chars, must start/end alphanumeric';
  }
  if (siblings.filter((d) => d.name === draft.name).length > 1) {
    return 'Names must be unique within a tenant';
  }
  const budget = nameBudget(draft.kind);
  const combined = platformName.length + draft.name.length;
  if (platformName && combined > budget) {
    return `Tenant name + datastore name is ${combined} chars, over the ${budget}-char budget for ${draft.kind}`;
  }
  // A DynamoDB table has no default partition key, so the CRD requires one.
  if (draft.kind === 'keyValue' && !draft.partitionKeyName) {
    return 'A keyValue store needs a partition key';
  }
  return null;
}

/** Folds the drafts into the `datastores` value the Platform CR expects. */
export function toDatastores(drafts: DatastoreDraft[]): Record<string, unknown>[] {
  return drafts.map((d) => {
    const entry: Record<string, unknown> = {
      name: d.name,
      kind: d.kind,
      deletionPolicy: d.deletionPolicy,
    };
    if (d.kind === 'keyValue') {
      entry.keyValue = {
        partitionKey: { name: d.partitionKeyName, type: d.partitionKeyType },
      };
    }
    return entry;
  });
}

/** Reads a template's default_values back into editable drafts. */
export function fromDatastores(raw: unknown): DatastoreDraft[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item) => {
    if (typeof item !== 'object' || item === null) return [];
    const entry = item as Record<string, unknown>;
    if (typeof entry.kind !== 'string' || !isDatastoreKind(entry.kind)) return [];
    const keyValue = (entry.keyValue ?? {}) as Record<string, unknown>;
    const partitionKey = (keyValue.partitionKey ?? {}) as Record<string, unknown>;
    const type = partitionKey.type;
    return [
      {
        name: typeof entry.name === 'string' ? entry.name : '',
        kind: entry.kind,
        deletionPolicy: entry.deletionPolicy === 'Delete' ? 'Delete' : 'Retain',
        partitionKeyName: typeof partitionKey.name === 'string' ? partitionKey.name : '',
        partitionKeyType:
          typeof type === 'string' && (ATTRIBUTE_TYPES as readonly string[]).includes(type)
            ? (type as AttributeType)
            : 'S',
      },
    ];
  });
}
