// Build the helm-values overrides object for a template-mode tenant create.
// This is the security boundary between the form and the POST body, so the two
// invariants live here where they can be unit-tested directly:
//
//   1. Allowlist — only paths the template marks in `allowedPaths` are emitted.
//      A field the template locks can never reach the payload, even if the form
//      somehow surfaced an edited value for it.
//   2. Prototype-pollution guard — a null-prototype accumulator plus rejection
//      of any `__proto__` / `constructor` / `prototype` path segment, so a
//      hostile path can't walk up to Object.prototype.
//
// `entries` are (dotted-path, value) pairs; later entries into the same parent
// merge rather than replace.
export function buildOverrides(
  allowedPaths: readonly string[],
  entries: ReadonlyArray<readonly [string, unknown]>,
): Record<string, unknown> {
  const overrides: Record<string, unknown> = Object.create(null);
  for (const [path, value] of entries) {
    if (!allowedPaths.includes(path)) continue;
    const segments = path.split(".");
    if (
      segments.some(
        (s) => s === "__proto__" || s === "constructor" || s === "prototype",
      )
    ) {
      continue;
    }
    let cur = overrides;
    for (let i = 0; i < segments.length - 1; i++) {
      const seg = segments[i];
      if (!(seg in cur) || typeof cur[seg] !== "object") {
        cur[seg] = Object.create(null) as Record<string, unknown>;
      }
      cur = cur[seg] as Record<string, unknown>;
    }
    cur[segments[segments.length - 1]] = value;
  }
  return overrides;
}
