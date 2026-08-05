#!/usr/bin/env bash
#
# Vendor the eks-agent-platform CRD schemas that portal validates a rendered
# tenant manifest against, and verify the vendored copies against upstream at the
# pinned ref.
#
#   scripts/crd.sh sync [<sha>]   # re-vendor (optionally moving the pin) + rewrite the digests
#   scripts/crd.sh check          # the blocking CI gate
#   scripts/crd.sh freshness      # is the pin behind upstream? (scheduled; exit 2 = behind)
#
# Why the copies exist at all is in internal/tenantmanifest/schemas/README.md.
# The short version: resolving them over the network at write time would make
# every verdict depend on whatever upstream was that minute, and the tempting
# failure handler — skip when unreachable — is the exact bug the check exists to
# catch.
#
# `check` is two assertions, not one. The digests prove the copies were not
# edited in place; they cannot prove the pin still describes them, because a ref
# moved by hand leaves the digests internally consistent and upstream-wrong.
#
# Upstream resolves two ways, both deterministic:
#   - $EKS_AGENT_PLATFORM_DIR — a local checkout. Under `check` its HEAD must
#     equal the pinned ref; a working tree on some other commit is an error
#     rather than a silent substitution.
#   - otherwise — raw.githubusercontent.com at the pinned ref.
#
# Every failure path exits non-zero. No path reports success without having
# compared something.
set -euo pipefail

readonly DIR="internal/tenantmanifest/schemas"
readonly MANIFEST="${DIR}/source.json"
readonly REPO="nanohype/eks-agent-platform"
readonly UPSTREAM_PATH="operators/config/crd/bases"

die() { echo "crd: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
need jq
need shasum

[[ -f "$MANIFEST" ]] || die "no ${MANIFEST} — run from the repo root"

pinned_ref()   { jq -r '.upstream.ref' "$MANIFEST"; }
declared()     { jq -r '.files[].file' "$MANIFEST"; }
declared_sha() { jq -r --arg f "$1" '.files[] | select(.file == $f) | .sha256' "$MANIFEST"; }
digest_of()    { shasum -a 256 "$1" | cut -d' ' -f1; }

# fetch <ref> <file> <dest> — one schema at <ref>, from the checkout or GitHub.
fetch() {
  local ref="$1" file="$2" dest="$3"
  if [[ -n "${EKS_AGENT_PLATFORM_DIR:-}" ]]; then
    [[ -d "${EKS_AGENT_PLATFORM_DIR}/.git" ]] \
      || die "EKS_AGENT_PLATFORM_DIR=${EKS_AGENT_PLATFORM_DIR} is not a git checkout"
    git -C "$EKS_AGENT_PLATFORM_DIR" cat-file -e "${ref}^{commit}" 2>/dev/null \
      || die "EKS_AGENT_PLATFORM_DIR does not contain commit ${ref} (fetch it first)"
    git -C "$EKS_AGENT_PLATFORM_DIR" show "${ref}:${UPSTREAM_PATH}/${file}" > "$dest" \
      || die "${UPSTREAM_PATH}/${file} not found at ${ref}"
  else
    need curl
    curl -fsSL "https://raw.githubusercontent.com/${REPO}/${ref}/${UPSTREAM_PATH}/${file}" -o "$dest" \
      || die "cannot read ${UPSTREAM_PATH}/${file} from ${REPO} at ${ref}"
  fi
  # A 404 body or an empty file is not a schema. Without this the digest
  # comparison would report a mismatch and hide the real cause.
  grep -q "kind: CustomResourceDefinition" "$dest" \
    || die "what came back for ${file} at ${ref} is not a CustomResourceDefinition"
}

cmd_sync() {
  local ref="${1:-$(pinned_ref)}" tmp updated
  tmp="$(mktemp)"; trap 'rm -f "$tmp"' RETURN

  updated="$(jq --arg r "$ref" '.upstream.ref = $r' "$MANIFEST")"
  while read -r file; do
    fetch "$ref" "$file" "$tmp"
    cp "$tmp" "${DIR}/${file}"
    local sum; sum="$(digest_of "${DIR}/${file}")"
    updated="$(jq --arg f "$file" --arg s "$sum" \
      '.files = [.files[] | if .file == $f then .sha256 = $s else . end]' <<<"$updated")"
    echo "crd: vendored ${file} (${sum:0:16})"
  done < <(declared)

  printf '%s\n' "$updated" > "$MANIFEST"
  echo "crd: pinned ${REPO}@${ref}"
  echo "crd: now run: go test ./internal/tenantmanifest/"
}

cmd_check() {
  local ref; ref="$(pinned_ref)"
  [[ -n "$ref" && "$ref" != "null" ]] || die "source.json has no upstream.ref"
  [[ "$ref" =~ ^[0-9a-f]{40}$ ]] || die "upstream.ref is ${ref}, not a full commit sha"

  # Every .yaml on disk must be declared. An undeclared schema is un-digested and
  # un-compared — the blind spot the manifest exists to remove.
  local present
  while read -r path; do
    present="$(basename "$path")"
    declared | grep -qxF "$present" \
      || die "${DIR}/${present} is not declared in source.json — add it with a digest, or delete it"
  done < <(find "$DIR" -maxdepth 1 -name '*.yaml')

  if [[ -n "${EKS_AGENT_PLATFORM_DIR:-}" ]]; then
    local head; head="$(git -C "$EKS_AGENT_PLATFORM_DIR" rev-parse HEAD)"
    [[ "$head" == "$ref" ]] \
      || die "EKS_AGENT_PLATFORM_DIR is on ${head} but the pin is ${ref}; check that commit out or unset the variable to read from GitHub"
  fi

  local tmp; tmp="$(mktemp)"; trap 'rm -f "$tmp"' RETURN
  local count=0
  while read -r file; do
    local sum; sum="$(declared_sha "$file")"
    [[ -n "$sum" && "$sum" != "null" ]] || die "source.json has no digest for ${file}"

    # 1. The copy is what the manifest says it is.
    [[ -f "${DIR}/${file}" ]] || die "${DIR}/${file} is missing — restore it with 'task crd:sync'"
    local actual; actual="$(digest_of "${DIR}/${file}")"
    [[ "$actual" == "$sum" ]] \
      || die "${file} digest ${actual} != source.json ${sum} — the vendored copy was edited; re-vendor with 'task crd:sync'"

    # 2. The manifest describes the commit it claims to.
    fetch "$ref" "$file" "$tmp"
    local upstream; upstream="$(digest_of "$tmp")"
    [[ "$upstream" == "$sum" ]] \
      || die "${REPO}@${ref} has ${file} at digest ${upstream}, the pin records ${sum} — the ref and the bytes disagree; re-vendor with 'task crd:sync'"
    count=$((count + 1))
  done < <(declared)

  [[ "$count" -gt 0 ]] || die "compared no schemas — source.json declares none"
  echo "crd: ${count} vendored schema(s) match ${REPO}@${ref}"
}

cmd_freshness() {
  local ref head
  ref="$(pinned_ref)"
  need curl
  head="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/${REPO}/commits?path=${UPSTREAM_PATH}&per_page=1" | jq -r '.[0].sha')" \
    || die "cannot ask GitHub for the newest ${UPSTREAM_PATH}"
  [[ -n "$head" && "$head" != "null" ]] || die "GitHub returned no commit for ${UPSTREAM_PATH}"

  if [[ "$head" == "$ref" ]]; then
    echo "crd: pin is current (${ref})"
    return 0
  fi
  # Exit 2, not 1, and the distinction is load-bearing. `die` exits 1 for every
  # way this check can BREAK — no curl, GitHub unreachable, a ref that resolves
  # to nothing. "The pin is behind" is a different fact from "I could not find
  # out", and the scheduled workflow files an issue for the first while failing
  # loudly for the second. Collapsing them would let a week of failed lookups
  # read as a week of confirmed drift, or the reverse.
  echo "crd: pin ${ref} is behind — ${UPSTREAM_PATH} last changed at ${head}" >&2
  echo "crd: re-vendor with: task crd:sync -- ${head}" >&2
  return 2
}

case "${1:-}" in
  sync)      shift; cmd_sync "$@" ;;
  check)     cmd_check ;;
  freshness) cmd_freshness ;;
  *)         die "usage: scripts/crd.sh {sync [<sha>]|check|freshness}" ;;
esac
