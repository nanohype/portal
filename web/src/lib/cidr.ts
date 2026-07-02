export const CIDR_RE = /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/;

// "203.0.113.0/24, 198.51.100.7/32" → ["203.0.113.0/24", "198.51.100.7/32"]
export function parseCidrList(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}
