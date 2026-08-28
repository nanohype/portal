// Gates over the generated API contracts, run in CI beside the drift check.
//
// The drift check asserts the generated files match the spec. These assert two
// things the compiler cannot: that the new contract still covers the operations
// the old one declares while both are in the tree, and that the two operations
// whose success type is a string index signature are not consumed as plain JSON.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const SRC = join(process.cwd(), 'src');

// Generated contracts name every operation by definition; the gates below are
// about what CONSUMES them.
const GENERATED = ['api/types.ts'];

// A Blob is read from a response the contract declares as a JSON object, so a
// plain-object read of `data` compiles and is wrong. 105 of the 107 operations
// fail loudly on that mistake; these two carry a string index signature and do
// not, so they are confined to the wrapper that reconciles the two types once.
const INDEX_SIGNATURE_OPERATIONS = ['downloadStateVersion', 'githubWebhook'];
const BLOB_WRAPPER = 'api/blob.ts';

function sources(dir, acc = []) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry !== 'gen') sources(full, acc);
      continue;
    }
    if (/\.tsx?$/.test(entry)) acc.push(full);
  }
  return acc;
}

const files = sources(SRC)
  .map((f) => ({ path: f.slice(SRC.length + 1), text: readFileSync(f, 'utf8') }))
  .filter((f) => !GENERATED.includes(f.path));

const oldContract = readFileSync(join(SRC, 'api/types.ts'), 'utf8');
const newSdk = readFileSync(join(SRC, 'api/gen/sdk.gen.ts'), 'utf8');

const oldOperations = [
  ...(/export interface operations \{([\s\S]*)\n\}/.exec(oldContract)?.[1] ?? '').matchAll(
    /^ {4}(\w+): \{/gm,
  ),
].map((m) => m[1]);
const newOperations = [...newSdk.matchAll(/^export const (\w+) = /gm)].map((m) => m[1]);

const failures = [];
const check = (name, offenders) => {
  if (offenders.length === 0) {
    console.log(`  ok   ${name}`);
    return;
  }
  failures.push(name);
  console.log(`  FAIL ${name}`);
  for (const o of offenders) console.log(`         ${o}`);
};

console.log('== contract gates ==');

if (oldOperations.length < 100) {
  failures.push('operation parse');
  console.log(`  FAIL parsed only ${oldOperations.length} operations from the previous contract`);
}

// hey-api normalizes acronym casing (getRunPlanJSON -> getRunPlanJson), so an
// operation is matched by identity rather than by spelling.
const lowerNew = new Set(newOperations.map((o) => o.toLowerCase()));
const lowerOld = new Set(oldOperations.map((o) => o.toLowerCase()));
check(
  `every operation in the previous contract is covered (${oldOperations.length})`,
  oldOperations.filter((o) => !lowerNew.has(o.toLowerCase())),
);
check(
  `no operation is added that the previous contract does not declare (${newOperations.length})`,
  newOperations.filter((o) => !lowerOld.has(o.toLowerCase())),
);

for (const operation of INDEX_SIGNATURE_OPERATIONS) {
  check(
    `${operation} is confined to ${BLOB_WRAPPER}`,
    files
      .filter((f) => f.path !== BLOB_WRAPPER && new RegExp(`\\b${operation}\\b`).test(f.text))
      .map((f) => f.path),
  );
}

// buildClientParams writes a caller-supplied key straight into a params slot
// after stripping a `$query_` / `$path_` style prefix, so the key
// `$query___proto__` substitutes the prototype chain of the object it returns.
// The generated SDK never calls it and the bundler drops it, which is what keeps
// the advisory inert here — so the gate is that nothing starts calling it.
check(
  'buildClientParams is not referenced',
  files.filter((f) => /\bbuildClientParams\b/.test(f.text)).map((f) => f.path),
);

// hey-api declares `response?: Response` where openapi-fetch declared it
// required, so `res.response.status` yields `number | undefined` rather than a
// compile error. The first instance would be silent, so none is allowed.
check(
  'no request result is read through .response',
  files.flatMap((f) =>
    f.text
      .split('\n')
      .map((line, i) => ({ line, n: i + 1 }))
      .filter(({ line }) => /(?<!on)\.response\b/.test(line) && !/onResponse/.test(line))
      .map(({ n }) => `${f.path}:${n}`),
  ),
);

if (failures.length > 0) {
  console.log(`== ${failures.length} contract gate(s) NOT met ==`);
  process.exit(1);
}
console.log('== all contract gates met ==');
