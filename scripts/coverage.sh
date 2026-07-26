#!/usr/bin/env bash
#
# coverage.sh — run the Go suite with coverage and enforce the floors.
#
# Two kinds of floor, both declared in .coverage-floors:
#
#   package <pkg> <pct>   the statement floor for a package
#   file    <path> <pct>  the statement floor for one file
#
# The per-file kind exists for the security-critical path — auth, the audit
# ledger, the approval gate, secret handling, signature verification. A package
# floor averages those files with everything around them, so a package can sit
# comfortably above its floor while an uncovered branch in the RBAC check ships.
# Those files are pinned at 100 and the gate reads them individually.
#
# Fails if:
#   - a package or file falls below its floor;
#   - a floored package or file produces no coverage data (stale/typo'd path — its
#     floor would otherwise be silently unenforced);
#   - a package reports coverage but has no floor (new code must be gated).
#
# Floors ratchet: raise a package's floor when you raise its coverage.
#
# The DB tier: the repository/service/handler integration tests skip without
# TEST_DATABASE_URL, and skipped tests lower coverage rather than failing. CI sets
# it against its Postgres service; locally, `task db:up` then export it, or expect
# those packages to come in under their floors.
#
# Run locally the same way CI does: scripts/coverage.sh
set -euo pipefail

cd "$(dirname "$0")/.."

profile="${1:-coverage.out}"
floors_file=".coverage-floors"
module="github.com/nanohype/portal"

# web/node_modules ships a vendored Go package (flatted/golang), which ./... walks
# into — it is a third-party npm artifact, not portal source, and it would both
# pollute the profile and trip the "reports coverage but has no floor" check.
packages=$(go list ./... | grep -v '/node_modules/')

out=$(go test $packages -coverprofile="$profile" -covermode=atomic -count=1)
echo "$out"

total=$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
echo "== total coverage: ${total}% =="

# Per-file statement coverage, aggregated from the per-function report: sum the
# statements each function contributes and the ones it covers, keyed by file.
# `go tool cover -func` prints "<file>:<line>:\t<func>\t<pct>%", which has no
# statement counts, so the profile itself is the source — mode line dropped, then
# "<file>:<range> <numstmt> <count>" summed per file.
#
# A block whose source carries //coverage:ignore counts as covered. It is the
# escape hatch for a branch that cannot be reached — `cipher.NewGCM` after a
# validated 32-byte AES key, say — where the alternative is either a permanently
# red gate or deleting a defensive error check. Every marker states why, and the
# gate reports how many were honoured so they cannot pile up unnoticed.
file_cov=$(awk -v m="${module}/" -v root="$PWD" '
  NR > 1 {
    split($1, a, ":"); f = a[1]
    sub(m, "", f)
    split($1, r, ":"); split(r[2], se, ",")
    split(se[1], s1, "."); split(se[2], s2, ".")
    start[NR] = s1[1]; end[NR] = s2[1]; file[NR] = f; stmts[NR] = $2; cnt[NR] = $3
    files[f] = 1
  }
  END {
    # Slurp each file once and record which lines carry the ignore marker.
    for (f in files) {
      n = 0
      while ((getline line < (root "/" f)) > 0) {
        n++
        if (index(line, "//coverage:ignore") > 0) mark[f, n] = 1
      }
      close(root "/" f)
    }
    for (i in file) {
      f = file[i]
      total[f] += stmts[i]
      ok = (cnt[i] > 0)
      if (!ok) {
        for (l = start[i]; l <= end[i]; l++) if (mark[f, l]) { ok = 1; ignores[f] += stmts[i]; break }
      }
      if (ok) covered[f] += stmts[i]
    }
    for (f in total) printf "%s %.1f %d\n", f, (covered[f] * 100.0) / total[f], ignores[f] + 0
  }
' "$profile")

fail=0
floored_pkgs=" "

while read -r line; do
  # Strip trailing comments so a floor may carry its justification inline.
  line="${line%%#*}"
  read -r kind target floor <<<"$line"
  [ -n "${kind:-}" ] || continue

  case "$kind" in
  package)
    floored_pkgs="${floored_pkgs}${target} "
    line=$(printf '%s\n' "$out" | grep -E "[[:space:]]${module}/${target}[[:space:]]" || true)
    cov=$(printf '%s\n' "$line" | grep -oE "coverage: [0-9.]+%" | grep -oE "[0-9.]+" | head -1 || true)
    label="package ${target}"
    ;;
  file)
    cov=$(printf '%s\n' "$file_cov" | awk -v f="$target" '$1 == f {print $2}')
    ign=$(printf '%s\n' "$file_cov" | awk -v f="$target" '$1 == f {print $3}')
    label="file    ${target}"
    [ "${ign:-0}" -gt 0 ] 2>/dev/null && label="${label} (${ign} stmt ignored)"
    ;;
  *)
    echo "::error::${floors_file}: unknown floor kind '${kind}' (want 'package' or 'file')"
    fail=1
    continue
    ;;
  esac

  if [ -z "$cov" ]; then
    echo "::error::floored ${kind} ${target} produced no coverage data (stale path, or no test files?)"
    fail=1
    continue
  fi
  if awk "BEGIN{exit !($cov < $floor)}"; then
    echo "::error::${label} coverage ${cov}% is below its ${floor}% floor"
    fail=1
  else
    printf '  ok  %-52s %5s%% >= %s%%\n' "$label" "$cov" "$floor"
  fi
done <"$floors_file"

# Any package that reports coverage but isn't floored is ungated — fail so new
# tested code can't land without a floor.
covered=$(printf '%s\n' "$out" | awk -v m="${module}/" '/coverage: [0-9]/ {for (i=1;i<=NF;i++) if (index($i,m)==1) {sub(m,"",$i); print $i}}' | sort -u)
for pkg in $covered; do
  case "$floored_pkgs" in
  *" $pkg "*) ;;
  *)
    echo "::error::package ${pkg} reports coverage but has no floor in ${floors_file}"
    fail=1
    ;;
  esac
done

if [ "$fail" -ne 0 ]; then
  echo "== coverage floors NOT met =="
  exit 1
fi
echo "== all coverage floors met =="
