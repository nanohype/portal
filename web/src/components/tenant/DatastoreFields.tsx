import { X, Plus } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import {
  ATTRIBUTE_TYPES,
  DATASTORE_KINDS,
  KIND_IMPLEMENTATIONS,
  datastoreError,
  emptyDatastore,
  isDatastoreKind,
  type AttributeType,
  type DatastoreDraft,
  type DatastoreKind,
} from '@/lib/datastores';

// The tenant's stateful substrate, declared row by row. Every other field in the
// create form is a scalar, so this is the one repeated group — and it is worth the
// UI rather than a JSON textarea, because the two rules that decide whether a
// declaration is legal (the composed-name budget and the DynamoDB partition key)
// are only checkable per row.
//
// A declaration grants the tenant role access to the resource and reports it under
// status.datastores. The resource itself is provisioned when the declaration
// reaches landing-zone's tenant-substrate input — so the field's hint says so,
// rather than implying a bucket appears on submit.

const TYPE_LABELS: Record<AttributeType, string> = {
  S: 'S · string',
  N: 'N · number',
  B: 'B · binary',
};

export function DatastoreFields({
  value,
  onChange,
  platformName,
  allowedKinds,
  disabled,
}: {
  value: DatastoreDraft[];
  onChange: (next: DatastoreDraft[]) => void;
  platformName: string;
  /**
   * Template cap; empty means the template places no restriction. Optional
   * because the UI can be a deploy ahead of the API that serves the field.
   */
  allowedKinds?: readonly string[];
  disabled?: boolean;
}) {
  const capped = (allowedKinds ?? []).filter(isDatastoreKind);
  const kinds: readonly DatastoreKind[] = capped.length > 0 ? capped : DATASTORE_KINDS;

  const update = (index: number, patch: Partial<DatastoreDraft>) => {
    onChange(value.map((d, i) => (i === index ? { ...d, ...patch } : d)));
  };

  return (
    <div className="space-y-2">
      {value.length === 0 && (
        <p className="text-[11px] text-muted-foreground/70">
          No datastores. Add one to declare a database, bucket, queue, cache, or stream for this
          tenant.
        </p>
      )}

      {value.map((draft, index) => {
        const error = datastoreError(draft, platformName, value);
        return (
          <div
            key={index}
            className="rounded-md border border-border/40 px-3 py-2.5 flex flex-col gap-2"
          >
            <div className="flex items-start gap-2">
              <Input
                value={draft.name}
                onChange={(e) => update(index, { name: e.target.value })}
                placeholder="corpus"
                aria-label={`Datastore ${index + 1} name`}
                className="font-mono"
                disabled={disabled}
              />
              <Select
                value={draft.kind}
                onChange={(e) => update(index, { kind: e.target.value as DatastoreKind })}
                aria-label={`Datastore ${index + 1} kind`}
                disabled={disabled}
              >
                {kinds.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </Select>
              <Select
                value={draft.deletionPolicy}
                onChange={(e) =>
                  update(index, { deletionPolicy: e.target.value as 'Retain' | 'Delete' })
                }
                aria-label={`Datastore ${index + 1} deletion policy`}
                disabled={disabled}
              >
                <option value="Retain">Retain</option>
                <option value="Delete">Delete</option>
              </Select>
              <button
                type="button"
                onClick={() => onChange(value.filter((_, i) => i !== index))}
                aria-label={`Remove datastore ${draft.name || index + 1}`}
                disabled={disabled}
                className="text-muted-foreground hover:text-destructive transition-colors cursor-pointer p-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>

            {draft.kind === 'keyValue' && (
              <div className="flex items-center gap-2">
                <Input
                  value={draft.partitionKeyName}
                  onChange={(e) => update(index, { partitionKeyName: e.target.value })}
                  placeholder="partition key (e.g. sessionId)"
                  aria-label={`Datastore ${index + 1} partition key`}
                  className="font-mono"
                  disabled={disabled}
                />
                <Select
                  value={draft.partitionKeyType}
                  onChange={(e) =>
                    update(index, { partitionKeyType: e.target.value as AttributeType })
                  }
                  aria-label={`Datastore ${index + 1} partition key type`}
                  disabled={disabled}
                >
                  {ATTRIBUTE_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {TYPE_LABELS[t]}
                    </option>
                  ))}
                </Select>
              </div>
            )}

            {error ? (
              <p className="text-[11px] text-destructive">{error}</p>
            ) : (
              <p className="text-[11px] text-muted-foreground/70">
                {KIND_IMPLEMENTATIONS[draft.kind]} ·{' '}
                {draft.deletionPolicy === 'Retain'
                  ? 'kept if the tenant is deleted'
                  : 'torn down with the tenant'}
              </p>
            )}
          </div>
        );
      })}

      <button
        type="button"
        onClick={() => onChange([...value, emptyDatastore(kinds[0])])}
        disabled={disabled}
        className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground hover:text-foreground transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <Plus className="h-3 w-3" />
        Add datastore
      </button>
    </div>
  );
}
