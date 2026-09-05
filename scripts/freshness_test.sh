#!/usr/bin/env bash
#
# Asserts what the freshness reports say, by running them.
#
# The report is copied verbatim into an issue body that is re-edited weekly and
# read on whatever day someone opens it. A commit id resolved at run time and
# printed there is correct until the next run and is presented as current for as
# long as the issue stays open — which is how a remediation instruction comes to
# name a commit that is no longer the newest.
#
# So the property is about the emitted bytes, not about the code that builds
# them: no upstream commit id may appear in what the report prints. Each case
# builds a real upstream repository whose HEAD is a commit this test knows, runs
# the script against it through the checkout seam the scripts already declare,
# and looks for that commit in the output.
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failures=0

ok()   { printf '    ok    %s\n' "$1"; }
fail() { printf '    FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# upstream <dir> <path> <marker> — a git repo whose newest commit touches <path>.
# Prints the sha of that commit.
upstream() {
  local dir="$1" path="$2" marker="$3"
  mkdir -p "$(dirname "${dir}/${path}")"
  git -C "$dir" init -q 2>/dev/null || { git init -q "$dir"; }
  git -C "$dir" config user.email t@t.local
  git -C "$dir" config user.name t
  printf '%s\n' "$marker" > "${dir}/${path}"
  git -C "$dir" add -A >/dev/null
  git -C "$dir" commit -qm "first"
  printf 'moved\n' >> "${dir}/${path}"
  git -C "$dir" add -A >/dev/null
  git -C "$dir" commit -qm "second"
  git -C "$dir" rev-parse HEAD
}

# case <label> <script> <dir-var> <schema-dir> <upstream-path>
run_case() {
  local label="$1" script="$2" dirvar="$3" schemadir="$4" upath="$5"
  local work up head out code
  work="$(mktemp -d)"; up="${work}/upstream"; mkdir -p "$up"

  head="$(upstream "$up" "$upath" "kind: CustomResourceDefinition")"

  # A tree the script can run in: its own schema directory, pinned to a commit
  # that is not upstream HEAD.
  mkdir -p "${work}/tree/$(dirname "$schemadir")" "${work}/tree/scripts"
  cp -R "${root}/${schemadir}" "${work}/tree/${schemadir}"
  cp "${root}/${script}" "${work}/tree/${script}"
  local stale="0000000000000000000000000000000000000000"
  jq --arg r "$stale" '.upstream.ref = $r' "${root}/${schemadir}/source.json" \
    > "${work}/tree/${schemadir}/source.json"

  out="$(cd "${work}/tree" && env "${dirvar}=${up}" "./${script}" freshness 2>&1)"
  code=$?

  if [[ "$code" -ne 2 ]]; then
    fail "${label}: exit ${code}, want 2 (behind) — the fixture did not reach the verdict"
    printf '          %s\n' "$out"
    rm -rf "$work"; return
  fi
  ok "${label}: reports the pin is behind"

  if grep -qE '[0-9a-f]{40}' <<<"${out//$stale/}"; then
    fail "${label}: the report names a commit id, which is stale the moment upstream moves"
    printf '          %s\n' "$out"
  else
    ok "${label}: names no upstream commit id"
  fi

  if grep -q -- "-- latest" <<<"$out"; then
    ok "${label}: the remediation resolves upstream HEAD when it runs"
  else
    fail "${label}: the remediation does not name a command that resolves HEAD"
    printf '          %s\n' "$out"
  fi

  # And the command it names has to exist: `sync latest` must resolve to the
  # head this test built, not to the stale pin it was given.
  local synced
  synced="$(cd "${work}/tree" && env "${dirvar}=${up}" "./${script}" sync latest 2>&1)"
  if grep -q "$head" <<<"$synced"; then
    ok "${label}: sync latest resolves to upstream HEAD"
  else
    fail "${label}: sync latest did not resolve to ${head}"
    printf '          %s\n' "$synced"
  fi
  rm -rf "$work"
}

echo "  freshness reports:"
run_case "crd" "scripts/crd.sh" "EKS_AGENT_PLATFORM_DIR" \
  "internal/tenantmanifest/schemas" "operators/config/crd/bases/governance.nanohype.dev_budgetpolicies.yaml"
run_case "xrd" "scripts/xrd.sh" "EKS_FLEET_DIR" \
  "internal/clusterspec/testdata" "apis/cluster/definition.yaml"

if [[ "$failures" -gt 0 ]]; then
  echo "== ${failures} freshness assertion(s) do not hold =="
  exit 1
fi
echo "== the freshness reports name a command rather than a commit =="
