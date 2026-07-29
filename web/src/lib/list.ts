// "a, b ,, c" → ["a", "b", "c"]
//
// The order-desk forms take every list as comma-separated text rather than a
// repeater widget: one input, no add/remove ceremony, and pasting a line out of
// `aws ec2 describe-subnets` output works. Trailing commas and stray whitespace
// are what that paste looks like, so both are absorbed rather than reported.
export function parseCommaList(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '');
}
