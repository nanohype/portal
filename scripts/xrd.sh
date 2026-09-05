#!/usr/bin/env bash
#
# Vendor the eks-fleet Cluster XRD that portal's rendered CR is checked against,
# and verify the vendored copy against upstream at the pinned ref.
#
#   scripts/xrd.sh sync [<sha>|latest]   # re-vendor (optionally moving the pin) + rewrite the digest
#   scripts/xrd.sh check          # the blocking CI gate
#   scripts/xrd.sh freshness      # is the pin behind upstream? (scheduled; exit 2 = behind)
#
# Why the copy exists at all, and why `check` is two assertions rather than one,
# is in internal/clusterspec/testdata/README.md.
#
# Upstream resolves two ways, both deterministic:
#   - $EKS_FLEET_DIR — a local checkout. Under `check` its HEAD must equal the
#     pinned ref; a working tree sitting on some other commit is an error rather
#     than a silent substitution.
#   - otherwise — raw.githubusercontent.com at the pinned ref.
#
# Every failure path exits non-zero. No path reports success without having
# compared something.
set -euo pipefail

readonly DIR="internal/clusterspec/testdata"
readonly MANIFEST="${DIR}/source.json"
readonly VENDORED="cluster-xrd.yaml"
readonly REPO="nanohype/eks-fleet"
readonly UPSTREAM_PATH="apis/cluster/definition.yaml"

die() { echo "xrd: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
need jq
need shasum

[[ -f "$MANIFEST" ]] || die "no ${MANIFEST} — run from the repo root"

pinned_ref() { jq -r '.upstream.ref' "$MANIFEST"; }
pinned_sha() { jq -r --arg f "$VENDORED" '.files[] | select(.file == $f) | .sha256' "$MANIFEST"; }
digest_of()  { shasum -a 256 "$1" | cut -d' ' -f1; }

# fetch <ref> <dest> — the schema at <ref>, from the local checkout or GitHub.
fetch() {
  local ref="$1" dest="$2"
  if [[ -n "${EKS_FLEET_DIR:-}" ]]; then
    [[ -d "${EKS_FLEET_DIR}/.git" ]] || die "EKS_FLEET_DIR=${EKS_FLEET_DIR} is not a git checkout"
    git -C "$EKS_FLEET_DIR" cat-file -e "${ref}^{commit}" 2>/dev/null \
      || die "EKS_FLEET_DIR does not contain commit ${ref} (fetch it first)"
    git -C "$EKS_FLEET_DIR" show "${ref}:${UPSTREAM_PATH}" > "$dest" \
      || die "${UPSTREAM_PATH} not found at ${ref}"
  else
    need curl
    curl -fsSL "https://raw.githubusercontent.com/${REPO}/${ref}/${UPSTREAM_PATH}" -o "$dest" \
      || die "cannot read ${UPSTREAM_PATH} from ${REPO} at ${ref}"
  fi
  # A 404 body or an empty file is not a schema. Without this the digest
  # comparison below would simply report a mismatch and hide the real cause.
  grep -q "kind: CompositeResourceDefinition" "$dest" \
    || die "what came back from ${ref} is not the Cluster XRD"
}

# upstream_head — the newest commit touching the vendored path upstream.
#
# Resolved the same two ways `fetch` resolves file contents: from
# $EKS_FLEET_DIR when it names a checkout, and from GitHub otherwise. Sharing
# the seam is what lets the freshness verdict be driven against a real repository
# rather than only against whatever github.com says this minute.
upstream_head() {
  local head
  if [[ -n "${EKS_FLEET_DIR:-}" ]]; then
    [[ -d "${EKS_FLEET_DIR}/.git" ]] || die "EKS_FLEET_DIR=${EKS_FLEET_DIR} is not a git checkout"
    head="$(git -C "${EKS_FLEET_DIR}" log -1 --format=%H -- "$UPSTREAM_PATH")" \
      || die "cannot read the newest ${UPSTREAM_PATH} from EKS_FLEET_DIR"
  else
    need curl
    head="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
      "https://api.github.com/repos/${REPO}/commits?path=${UPSTREAM_PATH}&per_page=1" | jq -r '.[0].sha')" \
      || die "cannot ask GitHub for the newest ${UPSTREAM_PATH}"
  fi
  [[ -n "$head" && "$head" != "null" ]] || die "no commit found for ${UPSTREAM_PATH} in ${REPO}"
  printf '%s\n' "$head"
}

cmd_sync() {
  # `latest` resolves upstream HEAD here, at the moment the re-vendor runs.
  # It is what the freshness report names, so that instruction stays correct
  # however long the issue carrying it has been open.
  local ref="${1:-$(pinned_ref)}" tmp
  [[ "$ref" == "latest" ]] && ref="$(upstream_head)"
  tmp="$(mktemp)"; trap 'rm -f "$tmp"' RETURN
  fetch "$ref" "$tmp"
  cp "$tmp" "${DIR}/${VENDORED}"

  local sum; sum="$(digest_of "${DIR}/${VENDORED}")"
  local updated; updated="$(jq --arg r "$ref" --arg f "$VENDORED" --arg s "$sum" \
    '.upstream.ref = $r | .files = [.files[] | if .file == $f then .sha256 = $s else . end]' "$MANIFEST")"
  printf '%s\n' "$updated" > "$MANIFEST"

  echo "xrd: vendored ${REPO}@${ref} (${sum})"
  echo "xrd: now run: go test ./internal/clusterspec/"
}

cmd_check() {
  local ref sum tmp
  ref="$(pinned_ref)"; sum="$(pinned_sha)"
  [[ -n "$ref" && "$ref" != "null" ]] || die "source.json has no upstream.ref"
  [[ -n "$sum" && "$sum" != "null" ]] || die "source.json has no digest for ${VENDORED}"

  # 1. The copy is what the manifest says it is.
  local actual; actual="$(digest_of "${DIR}/${VENDORED}")"
  [[ "$actual" == "$sum" ]] \
    || die "${VENDORED} digest ${actual} != source.json ${sum} — the vendored copy was edited; re-vendor with 'task xrd:sync'"

  # 2. The manifest describes the commit it claims to. A pin someone moved by
  #    hand still agrees with its own digest while describing a different schema.
  if [[ -n "${EKS_FLEET_DIR:-}" ]]; then
    local head; head="$(git -C "$EKS_FLEET_DIR" rev-parse HEAD)"
    [[ "$head" == "$ref" ]] \
      || die "EKS_FLEET_DIR is on ${head} but the pin is ${ref}; check that commit out or unset EKS_FLEET_DIR to read from GitHub"
  fi
  tmp="$(mktemp)"; trap 'rm -f "$tmp"' RETURN
  fetch "$ref" "$tmp"
  local upstream; upstream="$(digest_of "$tmp")"
  [[ "$upstream" == "$sum" ]] \
    || die "${REPO}@${ref} has digest ${upstream}, the pin records ${sum} — the ref and the bytes disagree; re-vendor with 'task xrd:sync'"

  echo "xrd: vendored schema matches ${REPO}@${ref}"
}

cmd_freshness() {
  local ref head
  ref="$(pinned_ref)"
  head="$(upstream_head)"

  if [[ "$head" == "$ref" ]]; then
    echo "xrd: pin is current (${ref})"
    return 0
  fi
  # Exit 2, not 1, and the distinction is load-bearing. `die` exits 1 for every
  # way this check can BREAK — no curl, GitHub unreachable, a ref that resolves
  # to nothing. "The pin is behind" is a different fact from "I could not find
  # out", and the scheduled workflow files an issue for the first while failing
  # loudly for the second. Collapsing them would let a week of failed lookups
  # read as a week of confirmed drift, or the reverse.
  # The remediation names a command, not a commit. This report is copied verbatim
  # into an issue body that is re-edited weekly and read on whatever day someone
  # opens it, so a resolved sha here is correct for at most a week and is presented
  # as current for as long as the issue is open. `latest` resolves when the
  # re-vendor runs, which is the only moment the answer is wanted.
  echo "xrd: pin ${ref} is behind — ${UPSTREAM_PATH} in ${REPO} has changed since it" >&2
  echo "xrd: re-vendor with: task xrd:sync -- latest" >&2
  return 2
}

case "${1:-}" in
  sync)      shift; cmd_sync "$@" ;;
  check)     cmd_check ;;
  freshness) cmd_freshness ;;
  *)         die "usage: scripts/xrd.sh {sync [<sha>|latest]|check|freshness}" ;;
esac
