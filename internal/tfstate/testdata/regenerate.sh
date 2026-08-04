#!/usr/bin/env bash
# Regenerates plan.json and plan.txt — the two halves of one real OpenTofu plan.
#
# The pair is the point. plan.json is what `tofu show -json` emits and what
# portal stores; plan.txt is what `tofu show` renders for a human. The test
# asserts that the plan portal serves discloses no value that plan.txt withholds,
# so the goldens have to come from the SAME plan, produced by a real tofu, or the
# comparison means nothing.
#
# Every value carries a sentinel naming what it is there to prove:
#
#   sentinel-var-default-AAAA   the outgoing sensitive value. tofu's own text
#                               renderer prints it in cleartext out of the
#                               computed `output` attribute, which the plan does
#                               NOT mark sensitive — so it survives. This one
#                               documents a hole in tofu's marking, not portal's.
#   sentinel-plain-BBBB         a non-sensitive variable used only by an output.
#                               Reachable in JSON through .variables,
#                               .planned_values, .output_changes, .prior_state
#                               and .configuration — and through none of the
#                               text. Whether it survives is the test of whether
#                               those sections are really dropped.
#   sentinel-unchanged-CCCC     a member that does not change, inside a map
#                               attribute that does. The text hides it behind
#                               "(1 unchanged attribute hidden)", so narrowing
#                               has to reach inside the attribute, not stop at
#                               its name.
#   sentinel-list-plain-DDDD    a non-sensitive value inside a list, to exercise
#                               the list-shaped sensitivity mirror.
#   sentinel-no-op-EEEE         held by a resource the plan is not touching.
#                               Reachable through the no-op entry in
#                               resource_changes and through .planned_values and
#                               .prior_state. In no text at all, because tofu
#                               does not render resources it is not acting on.
#   sentinel-rotated-FFFF       the incoming sensitive value. Marked sensitive
#                               everywhere it appears in resource_changes, and
#                               unmarked in .variables. If this survives, the
#                               endpoint is still leaking.
#
# The resources are chosen to cover the shapes the projection has to survive:
#
#   rotated       a map attribute where one member moves and one does not
#   nested        a sensitive value inside a list, for the list-shaped mirror
#   trigger_only  a changed attribute that is NOT echoed into a computed one.
#                 Every other terraform_data mirrors `input` into `output`
#                 unmarked, which means their secrets appear in the text no
#                 matter what — a fixture made only of those cannot tell a
#                 working redaction from a broken one.
#   steady        never changes, so resource_changes carries a no-op entry
#   doomed/fresh  a delete and a create, for the bare-bool mirror on the empty
#                 side (before_sensitive:false on a create, after_sensitive:false
#                 on a delete)
#   module.child  a resource inside a module, so module_address is populated
#
# The plan is built in two stages because a single apply cannot produce a create,
# an update and a delete at once: stage 1 establishes prior state, stage 2 rotates
# the secret, drops one resource and adds another.
#
# Usage: ./regenerate.sh   (needs `tofu` on PATH; writes plan.json + plan.txt here)

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/mod"

cat >"$work/mod/main.tf" <<'EOF'
variable "secret_in" {
  type      = string
  sensitive = true
}
resource "terraform_data" "inner" {
  input = { held = var.secret_in }
}
EOF

cat >"$work/main.tf" <<'EOF'
variable "rotating_secret" {
  type      = string
  sensitive = true
  default   = "sentinel-var-default-AAAA"
}

variable "plain_token" {
  type    = string
  default = "sentinel-plain-BBBB"
}

# updated in place: one member of a map attribute rotates, one is untouched
resource "terraform_data" "rotated" {
  input = {
    password  = var.rotating_secret
    untouched = "sentinel-unchanged-CCCC"
  }
}

# a sensitive value nested inside a list, to exercise the list-shaped mirror
resource "terraform_data" "nested" {
  input = [
    { name = "a", secret = var.rotating_secret },
    { name = "b", secret = "sentinel-list-plain-DDDD" },
  ]
}

# the changed attribute is not echoed into a computed one, so the only place its
# value could appear in the text is redacted
resource "terraform_data" "trigger_only" {
  triggers_replace = [var.rotating_secret]
}

# never changes: the plan carries a no-op entry for it, holding its whole
# attribute map, and the text does not mention it at all
resource "terraform_data" "steady" {
  input = { note = "sentinel-no-op-EEEE" }
}

# destroyed in stage 2: after is null, so the mirror sits only on before
resource "terraform_data" "doomed" {
  input = { farewell = var.rotating_secret }
}

module "child" {
  source    = "./mod"
  secret_in = var.rotating_secret
}

output "conn" {
  value     = "postgres://user:${var.rotating_secret}@host/db"
  sensitive = true
}

output "plain_out" {
  value = var.plain_token
}
EOF

cd "$work"
tofu init -no-color >/dev/null
tofu apply -auto-approve -no-color >/dev/null

# Stage 2: doomed leaves, fresh arrives, the secret rotates.
python3 - <<'PY'
p = "main.tf"
s = open(p).read()
s = s.replace(
    '''# destroyed in stage 2: after is null, so the mirror sits only on before
resource "terraform_data" "doomed" {
  input = { farewell = var.rotating_secret }
}''',
    '''# created: before is null, so before_sensitive is the bare bool false
resource "terraform_data" "fresh" {
  input = { greeting = var.rotating_secret }
}''',
)
open(p, "w").write(s)
PY

cat >terraform.tfvars <<'EOF'
rotating_secret = "sentinel-rotated-FFFF"
EOF

tofu plan -no-color -out=planfile >/dev/null
tofu show -json planfile >"$here/plan.json"
tofu show -no-color planfile >"$here/plan.txt"

echo "wrote $here/plan.json and $here/plan.txt from $(tofu version | head -1)"
