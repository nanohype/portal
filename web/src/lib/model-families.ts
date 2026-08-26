// The Platform CRD's model-family vocabulary: what a template may cap a tenant
// to, and what the template editor offers.
//
// ModelFamily is derived from the generated API contract rather than written out
// again, and MODEL_FAMILIES is held against it in both directions — `satisfies`
// rejects a value the contract does not have, and the exhaustiveness constant
// below fails to typecheck when the contract gains one this array omits. `tsc -b`
// runs in CI, so neither direction can drift quietly.
//
// The contract's enum is in turn asserted against the vendored Platform CRD by
// internal/tenantmanifest, so what this form offers is what the apiserver
// admits.

import type { CreateTemplateRequest } from '@/api/models';

export type ModelFamily = NonNullable<CreateTemplateRequest['allowed_model_families']>[number];

export const MODEL_FAMILIES = [
  'anthropic',
  'amazon-nova',
  'amazon-titan',
  'meta',
  'mistral',
  'cohere',
  'stability',
] as const satisfies readonly ModelFamily[];

type MissingModelFamilies = Exclude<ModelFamily, (typeof MODEL_FAMILIES)[number]>;
export const MODEL_FAMILIES_ARE_EXHAUSTIVE: [MissingModelFamilies] extends [never]
  ? true
  : ['model families the contract declares but MODEL_FAMILIES omits', MissingModelFamilies] = true;
