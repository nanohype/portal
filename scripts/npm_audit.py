#!/usr/bin/env python3
"""Fail on a high or critical npm advisory unless an unexpired entry waives it.

    npm_audit.py <tree>...        audit each npm tree, gate the union
    npm_audit.py selftest         run the gate against reports carrying each defect

`npm audit --audit-level=high` cannot set one advisory aside. When an advisory
has no patched version, the choices it leaves are a red build on every pull
request or a gate weakened for everything (a lower level, --omit=dev, a step
marked continue-on-error). This keeps the threshold and narrows the exception to
one advisory id, for a bounded time, with the argument for it written down.

WHAT THIS CHECKS

    Every advisory npm reports at high or critical, in any tree named, must be
    listed in the allowlist (.npm-audit-allowlist.json at the repository root).
    That is the threshold `--audit-level=high` applied: an advisory below it is
    reported by npm and ignored here, as it was there.

    Every allowlist entry carries an id (a GHSA id), an expiry (an RFC 3339 UTC
    timestamp), and a reason (the exposure argument). An entry fails the gate:
      - once its expiry has passed, so the argument is re-read rather than kept;
      - if its expiry is more than MAX_HORIZON_DAYS out, because an entry that
        never comes due is an ignore without an expiry;
      - if it matches no advisory in any tree audited, so a waiver outliving its
        advisory is removed instead of waiting to cover a later one.

    The trees are audited in one invocation for that last reason: an entry needed
    by one tree is not stale because another tree does not need it.

FAILING CLOSED

    A gate that cannot read its input must not report the input clean. So this
    fails on: npm exiting other than 0 or 1; npm's JSON not parsing, carrying an
    `error` object, or not being audit report version 2; an advisory without a
    GHSA id or a severity it knows; a package whose high/critical severity no
    reported advisory explains; a dependency edge naming a package the report does
    not contain; npm's own counts disagreeing with the packages listed; and npm's
    exit status disagreeing with the report (exit 1 means it found something at
    high or above, so exit 1 with nothing there is npm failing for another reason).

WHAT THIS DOES NOT CHECK

    Whether the exposure argument is true. That is what the reason field is for,
    and what the expiry forces someone to re-read.
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ALLOWLIST = ROOT / ".npm-audit-allowlist.json"

SEVERITIES = ("info", "low", "moderate", "high", "critical")
GATED = {"high", "critical"}  # what --audit-level=high fails on
MAX_HORIZON_DAYS = 90

GHSA_URL = re.compile(r"^https://github\.com/advisories/(GHSA(?:-[23456789cfghjmpqrvwx]{4}){3})$")
GHSA_ID = re.compile(r"^GHSA(?:-[23456789cfghjmpqrvwx]{4}){3}$")
EXPIRY = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
ENTRY_KEYS = {"id", "expires", "reason"}


def run_npm(tree: str) -> tuple[int, str]:
    """Run npm audit in one tree. stderr passes through to the job log."""
    proc = subprocess.run(
        ["npm", "audit", "--json", "--audit-level=high"],
        cwd=tree, stdout=subprocess.PIPE, text=True, check=False)
    return proc.returncode, proc.stdout


# ── one tree's report ──────────────────────────────────────────────────────

def read_report(tree: str, code: int, stdout: str) -> tuple[dict[str, dict], list[str]]:
    """Return ({ghsa_id: advisory}, failures) for one tree.

    An advisory is {severity, title, packages}. Any failure here means the report
    could not be read, and the caller must not treat the tree as clean.
    """
    def bad(msg: str) -> tuple[dict, list[str]]:
        return {}, [f"{tree}: {msg}"]

    if code not in (0, 1):
        return bad(f"npm audit exited {code}, which is neither 'clean' (0) nor 'findings' (1)")
    try:
        report = json.loads(stdout)
    except json.JSONDecodeError as exc:
        return bad(f"npm audit output is not JSON ({exc})")
    if not isinstance(report, dict):
        return bad("npm audit output is not a JSON object")
    if "error" in report:
        err = report["error"]
        detail = err.get("summary") or err.get("code") if isinstance(err, dict) else err
        return bad(f"npm audit reported an error instead of a report: {detail}")
    if report.get("auditReportVersion") != 2:
        return bad(f"unexpected auditReportVersion {report.get('auditReportVersion')!r}; "
                   "this gate reads version 2")
    vulns = report.get("vulnerabilities")
    if not isinstance(vulns, dict):
        return bad("report has no 'vulnerabilities' object")

    failures: list[str] = []
    advisories: dict[str, dict] = {}
    edges: dict[str, list] = {}

    for name, vuln in vulns.items():
        sev = vuln.get("severity") if isinstance(vuln, dict) else None
        via = vuln.get("via") if isinstance(vuln, dict) else None
        if sev not in SEVERITIES or not isinstance(via, list) or not via:
            failures.append(f"{tree}: package {name!r} has no readable severity/via")
            continue
        edges[name] = via
        for item in via:
            if isinstance(item, str):
                if item not in vulns:
                    failures.append(f"{tree}: {name} is affected via {item!r}, "
                                    "which the report does not list")
                continue
            if not isinstance(item, dict):
                failures.append(f"{tree}: {name} has an unreadable via entry {item!r}")
                continue
            m = GHSA_URL.match(str(item.get("url", "")))
            asev = item.get("severity")
            if not m or asev not in SEVERITIES:
                failures.append(f"{tree}: advisory on {name} has no GHSA id or severity this "
                                f"gate can read (url={item.get('url')!r}, "
                                f"severity={asev!r})")
                continue
            adv = advisories.setdefault(m.group(1), {
                "severity": asev, "title": item.get("title", ""), "packages": set()})
            if SEVERITIES.index(asev) > SEVERITIES.index(adv["severity"]):
                adv["severity"] = asev
            adv["packages"].add(name)

    # Every package npm rates high or above has to trace to an advisory rated high
    # or above. A package whose severity nothing explains is a report this gate is
    # misreading, and an allowlist matched against a misread report proves nothing.
    def roots(name: str, seen: set[str]) -> set[str]:
        if name in seen:
            return set()
        seen.add(name)
        found: set[str] = set()
        for item in edges.get(name, []):
            if isinstance(item, str):
                found |= roots(item, seen)
            elif isinstance(item, dict):
                m = GHSA_URL.match(str(item.get("url", "")))
                if m:
                    found.add(m.group(1))
        return found

    gated_packages = [n for n, v in vulns.items()
                      if isinstance(v, dict) and v.get("severity") in GATED]
    for name in gated_packages:
        if not any(advisories.get(a, {}).get("severity") in GATED for a in roots(name, set())):
            failures.append(f"{tree}: {name} is rated {vulns[name]['severity']} but no "
                            "high/critical advisory in the report explains it")

    counts = report.get("metadata", {}).get("vulnerabilities", {})
    try:
        counted = int(counts.get("high", -1)) + int(counts.get("critical", -1))
    except (TypeError, ValueError):
        counted = -2
    if counted != len(gated_packages):
        failures.append(f"{tree}: npm counts {counts.get('high')!r} high + "
                        f"{counts.get('critical')!r} critical, but lists "
                        f"{len(gated_packages)} package(s) at that level")

    if code == 1 and not gated_packages:
        failures.append(f"{tree}: npm audit exited 1 with nothing at high or above — "
                        "it failed for a reason other than findings")
    if code == 0 and gated_packages:
        failures.append(f"{tree}: npm audit exited 0 but the report lists "
                        f"{len(gated_packages)} package(s) at high or above")

    return advisories, failures


# ── the allowlist ──────────────────────────────────────────────────────────

def read_allowlist(text: str, now: datetime) -> tuple[dict[str, dict], list[str]]:
    """Return ({id: entry}, failures). Expired and over-horizon entries fail."""
    try:
        entries = json.loads(text)
    except json.JSONDecodeError as exc:
        return {}, [f"allowlist: not JSON ({exc})"]
    if not isinstance(entries, list):
        return {}, ["allowlist: must be a JSON array of entries"]

    failures: list[str] = []
    allowed: dict[str, dict] = {}
    for i, entry in enumerate(entries):
        where = f"allowlist entry {i}"
        if not isinstance(entry, dict) or set(entry) != ENTRY_KEYS:
            failures.append(f"{where}: must carry exactly {sorted(ENTRY_KEYS)}")
            continue
        ident, expires, reason = entry["id"], entry["expires"], entry["reason"]
        if not isinstance(ident, str) or not GHSA_ID.match(ident):
            failures.append(f"{where}: id {ident!r} is not a GHSA id")
            continue
        where = f"allowlist {ident}"
        if ident in allowed:
            failures.append(f"{where}: listed twice")
            continue
        if not isinstance(reason, str) or not reason.strip():
            failures.append(f"{where}: reason is empty — the exposure argument is the entry")
            continue
        if not isinstance(expires, str) or not EXPIRY.match(expires):
            failures.append(f"{where}: expires {expires!r} is not YYYY-MM-DDTHH:MM:SSZ")
            continue
        try:
            when = datetime.strptime(expires, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
        except ValueError:
            failures.append(f"{where}: expires {expires!r} is not a real date")
            continue
        if now >= when:
            failures.append(f"{where}: expired {expires} — re-read the exposure argument "
                            "and either renew it with a new expiry or fix the dependency")
            continue
        if when - now > timedelta(days=MAX_HORIZON_DAYS):
            failures.append(f"{where}: expires {expires}, more than {MAX_HORIZON_DAYS} days "
                            "out — an entry that never comes due is an ignore without an expiry")
            continue
        allowed[ident] = {**entry, "when": when}
    return allowed, failures


# ── the gate ───────────────────────────────────────────────────────────────

def gate(trees: list[str], allowlist: Path, now: datetime, run=run_npm) -> tuple[int, str]:
    lines: list[str] = []
    failures: list[str] = []

    try:
        allow_text = allowlist.read_text()
    except OSError as exc:
        return 1, f"FAIL allowlist: cannot read {allowlist} ({exc})"
    allowed, allow_failures = read_allowlist(allow_text, now)
    failures += allow_failures
    # An entry that failed to parse still names an id; keep it out of the stale
    # check so it is reported once, for the reason it actually failed.
    try:
        named = {e.get("id") for e in json.loads(allow_text) if isinstance(e, dict)}
    except (json.JSONDecodeError, TypeError, AttributeError):
        named = set()

    matched: set[str] = set()
    for tree in trees:
        try:
            code, stdout = run(tree)
        except OSError as exc:
            failures.append(f"{tree}: could not run npm audit ({exc})")
            continue
        advisories, tree_failures = read_report(tree, code, stdout)
        failures += tree_failures
        if tree_failures:
            continue
        gated = {i: a for i, a in advisories.items() if a["severity"] in GATED}
        lines.append(f"{tree}: {len(gated)} advisory(ies) at high or above")
        for ident, adv in sorted(gated.items()):
            pkgs = ", ".join(sorted(adv["packages"]))
            matched.add(ident)
            if ident in allowed:
                lines.append(f"  allowed  {ident} ({adv['severity']}, {pkgs}) "
                             f"until {allowed[ident]['expires']}")
            elif ident in named:
                pass  # its allowlist entry already failed, with its own reason
            else:
                failures.append(f"{tree}: {ident} ({adv['severity']}, {pkgs}) is not in "
                                f"{allowlist.name}: {adv['title']}")

    for ident in sorted(allowed):
        if ident not in matched:
            failures.append(f"allowlist {ident}: matches no high/critical advisory in "
                            f"{', '.join(trees)} — remove it")

    out = "\n".join(lines + [f"FAIL {f}" for f in failures])
    if not failures:
        out += "\nnpm audit gate: pass"
    return (1 if failures else 0), out


# ── selftest ───────────────────────────────────────────────────────────────

def selftest() -> int:
    """Run the gate against reports carrying each defect it exists to catch.

    Each rejecting case requires a non-zero exit AND a named message: an exit code
    alone is satisfied by a gate that refuses everything, or by one that fails
    closed on its own fixture for the wrong reason. The accepting cases are the
    other half — a gate that fails correct work gets switched off.
    """
    import tempfile

    now = datetime(2026, 11, 17, 12, 0, 0, tzinfo=timezone.utc)
    ghsa_a = "GHSA-fc5q-39xc-vjpf"
    ghsa_b = "GHSA-2222-3333-4444"

    def adv(ghsa: str, sev: str, name: str = "pkg") -> dict:
        return {"source": 1, "name": name, "title": f"{name} is broken", "severity": sev,
                "url": f"https://github.com/advisories/{ghsa}", "range": "<=9"}

    def report(vulns: dict[str, tuple[str, list]]) -> str:
        counts = {s: 0 for s in SEVERITIES}
        for sev, _ in vulns.values():
            counts[sev] += 1
        return json.dumps({
            "auditReportVersion": 2,
            "vulnerabilities": {n: {"name": n, "severity": s, "via": v}
                                for n, (s, v) in vulns.items()},
            "metadata": {"vulnerabilities": {**counts, "total": len(vulns)}},
        })

    def entry(ghsa: str, expires: str = "2026-12-08T00:00:00Z", reason: str = "dev-only") -> dict:
        return {"id": ghsa, "expires": expires, "reason": reason}

    # The shape the real picomatch advisory takes: one advisory on the leaf,
    # transitive packages rated by it.
    picomatch = report({
        "picomatch": ("high", [adv(ghsa_a, "high", "picomatch")]),
        "tinyglobby": ("high", ["picomatch"]),
        "vite": ("high", ["picomatch", "tinyglobby"]),
    })
    clean = report({})
    critical = report({"x": ("critical", [adv(ghsa_b, "critical", "x")])})
    unlisted_high = report({
        "picomatch": ("high", [adv(ghsa_a, "high", "picomatch")]),
        "y": ("high", [adv(ghsa_b, "high", "y")]),
    })

    # (name, {tree: (exit, stdout)}, allowlist, must-name or None for a pass)
    cases = [
        # ── pass paths
        ("an allowlisted high advisory with its transitive dependents passes",
         {"web": (1, picomatch), "apigen": (0, clean)}, [entry(ghsa_a)], None),
        ("an unlisted moderate advisory is below the threshold, as with --audit-level=high",
         {"web": (0, report({"m": ("moderate", [adv(ghsa_b, "moderate", "m")])})),
          "apigen": (1, picomatch)}, [entry(ghsa_a)], None),
        ("an entry needed by only one of the trees is not stale",
         {"web": (0, clean), "apigen": (1, picomatch)}, [entry(ghsa_a)], None),

        # ── rejection paths the allowlist exists for
        ("an unlisted high advisory fails",
         {"web": (1, unlisted_high)}, [entry(ghsa_a)], f"{ghsa_b} (high, y) is not in"),
        ("an unlisted critical advisory fails",
         {"web": (1, critical)}, [], f"{ghsa_b} (critical, x) is not in"),
        ("an expired entry fails though it still matches",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2026-11-17T12:00:00Z")],
         f"allowlist {ghsa_a}: expired"),
        ("an entry matching nothing fails as stale",
         {"web": (0, clean)}, [entry(ghsa_a)], f"allowlist {ghsa_a}: matches no"),
        ("an entry with no real expiry fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2099-01-01T00:00:00Z")],
         "more than 90 days out"),
        ("an entry without a reason fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, reason="  ")], "reason is empty"),
        ("an entry carrying an extra key fails",
         {"web": (1, picomatch)}, [{**entry(ghsa_a), "package": "picomatch"}],
         "must carry exactly"),
        ("an entry with a date-only expiry fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2026-12-08")], "is not YYYY-MM-DDTHH:MM:SSZ"),

        # ── fail closed on what cannot be read
        ("npm output that is not JSON fails closed",
         {"web": (1, "npm ERR! something")}, [entry(ghsa_a)], "web: npm audit output is not JSON"),
        ("an npm error object fails closed",
         {"web": (1, json.dumps({"error": {"code": "ENOLOCK", "summary": "no lockfile"}}))},
         [], "no lockfile"),
        ("npm exiting 1 with nothing at high fails closed",
         {"web": (1, clean)}, [], "failed for a reason other than findings"),
        ("npm exiting with another status fails closed",
         {"web": (2, clean)}, [], "exited 2"),
        ("npm exiting 0 over a high finding fails closed",
         {"web": (0, picomatch)}, [entry(ghsa_a)], "exited 0 but the report lists"),
        ("npm not runnable fails closed",
         {"web": OSError("npm: not found")}, [], "could not run npm audit"),
        ("a report of another version fails closed",
         {"web": (0, json.dumps({"auditReportVersion": 1, "vulnerabilities": {}}))},
         [], "auditReportVersion"),
        ("an advisory without a GHSA id fails closed",
         {"web": (1, report({"z": ("high", [{**adv(ghsa_b, "high", "z"),
                                              "url": "https://example.com/1"}])}))},
         [], "no GHSA id"),
        ("a transitive edge to a package the report omits fails closed",
         {"web": (1, report({"vite": ("high", ["picomatch"])}))}, [],
         "which the report does not list"),
        ("a high package no high advisory explains fails closed",
         {"web": (1, report({"q": ("high", [adv(ghsa_b, "low", "q")])}))}, [],
         "no high/critical advisory in the report explains it"),
        ("npm's counts disagreeing with the packages listed fails closed",
         {"web": (1, picomatch.replace('"high": 3', '"high": 1'))}, [entry(ghsa_a)],
         "npm counts"),
        ("an allowlist that is not JSON fails closed",
         {"web": (1, picomatch)}, "not json", "allowlist: not JSON"),
    ]

    failed = 0
    with tempfile.TemporaryDirectory() as tmp:
        for name, trees, allow, must in cases:
            path = Path(tmp) / "allow.json"
            path.write_text(allow if isinstance(allow, str) else json.dumps(allow))

            def run(tree, trees=trees):
                result = trees[tree]
                if isinstance(result, Exception):
                    raise result
                return result

            code, out = gate(list(trees), path, now, run)
            if must is None:
                ok = code == 0 and "FAIL" not in out
            else:
                ok = code != 0 and must in out
            print(f"{'ok  ' if ok else 'FAIL'} {name}")
            if not ok:
                failed += 1
                print("     " + out.replace("\n", "\n     "))

        # The committed allowlist has to be one this gate can read. Its expiry is
        # deliberately not checked here: the live gate does that against the clock.
        _, problems = read_allowlist(ALLOWLIST.read_text(), datetime.min.replace(
            tzinfo=timezone.utc))
        shape = [p for p in problems if "more than" not in p]
        ok = not shape
        print(f"{'ok  ' if ok else 'FAIL'} the committed allowlist parses")
        if not ok:
            failed += 1
            print("     " + "\n     ".join(shape))

    print(f"\n{len(cases) + 1 - failed}/{len(cases) + 1} selftest cases passed")
    return 1 if failed else 0


def main() -> int:
    args = sys.argv[1:]
    if args == ["selftest"]:
        return selftest()
    if not args or any(a.startswith("-") for a in args):
        print(__doc__.split("\n\n")[1], file=sys.stderr)
        return 2
    code, out = gate(args, ALLOWLIST, datetime.now(timezone.utc))
    print(out)
    return code


if __name__ == "__main__":
    sys.exit(main())
