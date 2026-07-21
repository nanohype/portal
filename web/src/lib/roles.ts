// Role ordering, mirrored from the API's rbac package. The server is the only
// thing that decides a request; this exists so the UI stops offering controls
// whose call would come back 403.

export const roles = ['viewer', 'operator', 'admin', 'owner'] as const;

export type Role = (typeof roles)[number];

const level: Record<string, number> = {
  viewer: 1,
  operator: 2,
  admin: 3,
  owner: 4,
};

// roleAtLeast reports whether a role clears a bar. Anything unrecognised —
// undefined, empty, a role this build does not know — clears nothing, so a
// missing role hides controls rather than showing them.
export function roleAtLeast(role: string | null | undefined, min: Role): boolean {
  return (level[role ?? ''] ?? 0) >= level[min];
}
