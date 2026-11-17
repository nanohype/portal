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
    timestamp), a reason (the exposure argument), the trees the argument was
    written for, and dev_only (whether the argument rests on the vulnerable
    package being a development dependency). An entry fails the gate:
      - once its expiry has passed, so the argument is re-read rather than kept;
      - if its expiry is more than MAX_HORIZON_DAYS out, because an entry that
        never comes due is an ignore without an expiry;
      - if a tree it names no longer reports its advisory, so a waiver outliving
        its advisory is removed instead of waiting to cover a later one;
      - if it names a tree this run did not audit.

    The argument is scoped, and the gate holds the advisory to that scope rather
    than trusting it. An advisory is waived only in the trees its entry names: the
    same advisory reported in any other tree fails as unlisted there. With
    dev_only true, every lockfile node npm reports the advisory on must carry
    `"dev": true` in that tree's package-lock.json, which npm sets only on a
    package no production dependency reaches. So the dependency graph moving
    under an argument written for the old one fails the gate the day it moves,
    not on the expiry.

    The trees are audited in one invocation so every named tree is checked in
    the run that judges the entry.

FAILING CLOSED

    A gate that cannot read its input must not report the input clean. So this
    fails on: npm exiting other than 0 or 1; npm's JSON not parsing, carrying an
    `error` object, or not being audit report version 2; an advisory without a
    GHSA id or a severity it knows; a package whose high/critical severity no
    reported advisory explains; a dependency edge naming a package the report does
    not contain; a package without a readable list of lockfile nodes; npm's own
    counts disagreeing with the packages listed; npm's exit status disagreeing
    with the report (exit 1 means it found something at high or above, so exit 1
    with nothing there is npm failing for another reason); an allowlist that
    cannot be read; and, for a dev_only entry, a lockfile that cannot be read or
    does not list a node npm reported.

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
ENTRY_KEYS = {"id", "expires", "reason", "trees", "dev_only"}

# The whole dependency tree, at npm's own high threshold. The selftest pins this
# exact tuple: --omit=dev or a lower --audit-level here would narrow the gate for
# every advisory, and once the allowlist is empty nothing else would notice.
NPM_ARGV = ("npm", "audit", "--json", "--audit-level=high")


def run_npm(tree: str) -> tuple[int, str]:
    """Run npm audit in one tree. stderr passes through to the job log."""
    proc = subprocess.run(
        list(NPM_ARGV), cwd=tree, stdout=subprocess.PIPE, text=True, check=False)
    return proc.returncode, proc.stdout


def read_lockfile(tree: str) -> str:
    return (Path(tree) / "package-lock.json").read_text()


# ── one tree's report ──────────────────────────────────────────────────────

def read_report(tree: str, code: int, stdout: str) -> tuple[dict[str, dict], list[str]]:
    """Return ({ghsa_id: advisory}, failures) for one tree.

    An advisory is {severity, title, packages, nodes}: nodes are the lockfile
    paths of the packages the advisory is reported on directly. Any failure here
    means the report could not be read, and the caller must not treat the tree
    as clean.
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
        nodes = vuln.get("nodes") if isinstance(vuln, dict) else None
        if sev not in SEVERITIES or not isinstance(via, list) or not via:
            failures.append(f"{tree}: package {name!r} has no readable severity/via")
            continue
        if (not isinstance(nodes, list) or not nodes
                or not all(isinstance(n, str) and n for n in nodes)):
            failures.append(f"{tree}: package {name!r} has no readable list of lockfile nodes")
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
                "severity": asev, "title": item.get("title", ""), "packages": set(),
                "nodes": set()})
            if SEVERITIES.index(asev) > SEVERITIES.index(adv["severity"]):
                adv["severity"] = asev
            adv["packages"].add(name)
            adv["nodes"].update(nodes)

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

def read_allowlist(text: str, now: datetime) -> tuple[dict[str, dict], set[str], list[str]]:
    """Return ({id: entry}, rejected ids, failures).

    Expired and over-horizon entries fail. `rejected` holds the id of every entry
    that failed, and an id enters it only through reject(), which records the
    failure in the same call: the gate stays quiet about an advisory whose entry
    was rejected, and that silence is safe only because the rejection is already
    a failure.
    """
    try:
        entries = json.loads(text)
    except json.JSONDecodeError as exc:
        return {}, set(), [f"allowlist: not JSON ({exc})"]
    if not isinstance(entries, list):
        return {}, set(), ["allowlist: must be a JSON array of entries"]

    failures: list[str] = []
    rejected: set[str] = set()
    allowed: dict[str, dict] = {}

    def reject(ident: object, msg: str) -> None:
        failures.append(msg)
        if isinstance(ident, str):
            rejected.add(ident)

    for i, entry in enumerate(entries):
        where = f"allowlist entry {i}"
        if not isinstance(entry, dict) or set(entry) != ENTRY_KEYS:
            reject(entry.get("id") if isinstance(entry, dict) else None,
                   f"{where}: must carry exactly {sorted(ENTRY_KEYS)}")
            continue
        ident, expires, reason = entry["id"], entry["expires"], entry["reason"]
        trees, dev_only = entry["trees"], entry["dev_only"]
        if not isinstance(ident, str) or not GHSA_ID.match(ident):
            reject(ident, f"{where}: id {ident!r} is not a GHSA id")
            continue
        where = f"allowlist {ident}"
        if ident in allowed or ident in rejected:
            reject(ident, f"{where}: listed twice")
            continue
        if not isinstance(reason, str) or not reason.strip():
            reject(ident, f"{where}: reason is empty — the exposure argument is the entry")
            continue
        if (not isinstance(trees, list) or not trees
                or not all(isinstance(t, str) and t for t in trees)
                or len(set(trees)) != len(trees)):
            reject(ident, f"{where}: trees {trees!r} must be a non-empty list of distinct "
                          "tree paths — the trees the exposure argument was written for")
            continue
        if not isinstance(dev_only, bool):
            reject(ident, f"{where}: dev_only {dev_only!r} must be true or false")
            continue
        if not isinstance(expires, str) or not EXPIRY.match(expires):
            reject(ident, f"{where}: expires {expires!r} is not YYYY-MM-DDTHH:MM:SSZ")
            continue
        try:
            when = datetime.strptime(expires, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
        except ValueError:
            reject(ident, f"{where}: expires {expires!r} is not a real date")
            continue
        if now >= when:
            reject(ident, f"{where}: expired {expires} — re-read the exposure argument "
                          "and either renew it with a new expiry or fix the dependency")
            continue
        if when - now > timedelta(days=MAX_HORIZON_DAYS):
            reject(ident, f"{where}: expires {expires}, more than {MAX_HORIZON_DAYS} days "
                          "out — an entry that never comes due is an ignore without an expiry")
            continue
        allowed[ident] = {**entry, "when": when}
    return allowed, rejected, failures


def non_dev_nodes(lock_text: str, nodes: set[str]) -> tuple[list[str], str | None]:
    """Return (nodes not marked dev, error). error is set when the lockfile is unreadable."""
    try:
        lock = json.loads(lock_text)
    except json.JSONDecodeError as exc:
        return [], f"package-lock.json is not JSON ({exc})"
    packages = lock.get("packages") if isinstance(lock, dict) else None
    if not isinstance(packages, dict):
        return [], "package-lock.json has no 'packages' object (lockfileVersion 2 or 3)"
    bad = []
    for node in sorted(nodes):
        meta = packages.get(node)
        if not isinstance(meta, dict):
            return [], f"package-lock.json does not list {node}, which npm reported"
        if meta.get("dev") is not True:
            bad.append(node)
    return bad, None


# ── the gate ───────────────────────────────────────────────────────────────

def gate(trees: list[str], allowlist: Path, now: datetime, run=run_npm,
         lockfile=read_lockfile) -> tuple[int, str]:
    lines: list[str] = []
    failures: list[str] = []

    try:
        allow_text = allowlist.read_text()
    except OSError as exc:
        return 1, f"FAIL allowlist: cannot read {allowlist} ({exc})"
    allowed, rejected, allow_failures = read_allowlist(allow_text, now)
    failures += allow_failures

    for ident, entry in sorted(allowed.items()):
        unaudited = [t for t in entry["trees"] if t not in trees]
        if unaudited:
            failures.append(f"allowlist {ident}: names {', '.join(unaudited)}, which this "
                            f"run did not audit ({', '.join(trees)})")

    matched: set[tuple[str, str]] = set()
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
            matched.add((ident, tree))
            entry = allowed.get(ident)
            if entry is not None and tree in entry["trees"]:
                if entry["dev_only"]:
                    try:
                        bad, err = non_dev_nodes(lockfile(tree), adv["nodes"])
                    except OSError as exc:
                        bad, err = [], f"cannot read package-lock.json ({exc})"
                    if err:
                        failures.append(f"{tree}: {ident} is waived as dev_only, but {err}")
                        continue
                    if bad:
                        failures.append(f"{tree}: {ident} is waived as dev_only, but "
                                        f"{', '.join(bad)} is not a dev-only node in "
                                        "package-lock.json — the exposure argument no longer "
                                        "describes this tree")
                        continue
                lines.append(f"  allowed  {ident} ({adv['severity']}, {pkgs}) "
                             f"until {entry['expires']}")
            elif entry is not None:
                failures.append(f"{tree}: {ident} ({adv['severity']}, {pkgs}) is waived only "
                                f"for {', '.join(entry['trees'])} — the exposure argument "
                                f"does not cover {tree}")
            elif ident in rejected:
                pass  # read_allowlist already failed its entry, with its own reason
            else:
                failures.append(f"{tree}: {ident} ({adv['severity']}, {pkgs}) is not in "
                                f"{allowlist.name}: {adv['title']}")

    for ident, entry in sorted(allowed.items()):
        for tree in entry["trees"]:
            if tree in trees and (ident, tree) not in matched:
                failures.append(f"allowlist {ident}: matches no high/critical advisory in "
                                f"{tree} — remove {tree} from its trees, or the entry")

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
            "vulnerabilities": {n: {"name": n, "severity": s, "via": v,
                                    "nodes": [f"node_modules/{n}"]}
                                for n, (s, v) in vulns.items()},
            "metadata": {"vulnerabilities": {**counts, "total": len(vulns)}},
        })

    def entry(ghsa: str, expires: str = "2026-12-08T00:00:00Z", reason: str = "dev-only",
              trees: list | None = None, dev_only: object = True) -> dict:
        return {"id": ghsa, "expires": expires, "reason": reason,
                "trees": ["web"] if trees is None else trees, "dev_only": dev_only}

    def lock(**dev: bool) -> str:
        return json.dumps({"lockfileVersion": 3, "packages": {
            "": {"name": "fixture"},
            **{f"node_modules/{n}": ({"dev": True} if d else {}) for n, d in dev.items()}}})

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
    dev_lock = lock(picomatch=True, tinyglobby=True, vite=True, y=True, x=True)
    prod_lock = lock(picomatch=False, tinyglobby=True, vite=True)
    missing = Path("/nonexistent/npm-audit-selftest/allow.json")

    # (name, {tree: (exit, stdout)}, allowlist, must-name or None for a pass,
    #  {tree: lockfile text}; a tree absent from that map has no readable lockfile)
    cases = [
        # ── pass paths
        ("an allowlisted high advisory with its transitive dependents passes",
         {"web": (1, picomatch), "apigen": (0, clean)}, [entry(ghsa_a)], None,
         {"web": dev_lock}),
        ("an unlisted moderate advisory is below the threshold, as with --audit-level=high",
         {"web": (0, report({"m": ("moderate", [adv(ghsa_b, "moderate", "m")])})),
          "apigen": (1, picomatch)}, [entry(ghsa_a, trees=["apigen"])], None,
         {"apigen": dev_lock}),
        ("an entry scoped to the one tree that needs it is not stale",
         {"web": (0, clean), "apigen": (1, picomatch)}, [entry(ghsa_a, trees=["apigen"])],
         None, {"apigen": dev_lock}),
        ("an entry that does not claim dev_only does not read the lockfile",
         {"web": (1, picomatch)}, [entry(ghsa_a, dev_only=False)], None, {}),

        # ── rejection paths the allowlist exists for
        ("an unlisted high advisory fails",
         {"web": (1, unlisted_high)}, [entry(ghsa_a)], f"{ghsa_b} (high, y) is not in",
         {"web": dev_lock}),
        ("an unlisted critical advisory fails",
         {"web": (1, critical)}, [], f"{ghsa_b} (critical, x) is not in", {}),
        ("an expired entry fails though it still matches",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2026-11-17T12:00:00Z")],
         f"allowlist {ghsa_a}: expired", {"web": dev_lock}),
        ("an entry matching nothing fails as stale",
         {"web": (0, clean)}, [entry(ghsa_a)], f"allowlist {ghsa_a}: matches no", {}),
        ("an entry naming a tree that no longer reports it fails as stale for that tree",
         {"web": (1, picomatch), "apigen": (0, clean)}, [entry(ghsa_a, trees=["web", "apigen"])],
         "matches no high/critical advisory in apigen", {"web": dev_lock}),
        ("an entry naming a tree this run did not audit fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, trees=["web", "elsewhere"])],
         "names elsewhere, which this run did not audit", {"web": dev_lock}),
        ("an advisory reported in a tree its entry does not name fails",
         {"web": (1, picomatch), "apigen": (1, picomatch)}, [entry(ghsa_a)],
         "does not cover apigen", {"web": dev_lock, "apigen": dev_lock}),
        ("a dev_only entry fails once the vulnerable node is reachable from production",
         {"web": (1, picomatch)}, [entry(ghsa_a)],
         "node_modules/picomatch is not a dev-only node", {"web": prod_lock}),
        ("a dev_only entry fails closed when the lockfile cannot be read",
         {"web": (1, picomatch)}, [entry(ghsa_a)], "cannot read package-lock.json", {}),
        ("a dev_only entry fails closed when the lockfile is not JSON",
         {"web": (1, picomatch)}, [entry(ghsa_a)], "package-lock.json is not JSON",
         {"web": "{"}),
        ("a dev_only entry fails closed when the lockfile omits a reported node",
         {"web": (1, picomatch)}, [entry(ghsa_a)],
         "does not list node_modules/picomatch", {"web": lock(vite=True)}),
        ("an entry with no real expiry fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2099-01-01T00:00:00Z")],
         "more than 90 days out", {"web": dev_lock}),
        ("an expiry of the right shape that is not a real date fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2026-02-30T00:00:00Z")],
         "is not a real date", {"web": dev_lock}),
        ("an entry without a reason fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, reason="  ")], "reason is empty",
         {"web": dev_lock}),
        ("an entry carrying an extra key fails",
         {"web": (1, picomatch)}, [{**entry(ghsa_a), "package": "picomatch"}],
         "must carry exactly", {"web": dev_lock}),
        ("an entry with a date-only expiry fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, "2026-12-08")],
         "is not YYYY-MM-DDTHH:MM:SSZ", {"web": dev_lock}),
        ("an entry listed twice fails",
         {"web": (1, picomatch)}, [entry(ghsa_a), entry(ghsa_a)], "listed twice",
         {"web": dev_lock}),
        ("an entry whose id is not a GHSA id fails",
         {"web": (1, picomatch)}, [entry(ghsa_a), entry("CVE-2026-112366")],
         "is not a GHSA id", {"web": dev_lock}),
        ("an entry with no trees fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, trees=[])], "must be a non-empty list",
         {"web": dev_lock}),
        ("an entry whose dev_only is not a boolean fails",
         {"web": (1, picomatch)}, [entry(ghsa_a, dev_only="yes")], "must be true or false",
         {"web": dev_lock}),

        # ── fail closed on what cannot be read
        ("an allowlist that does not exist fails closed",
         {"web": (0, clean)}, missing, "cannot read", {}),
        ("npm output that is not JSON fails closed",
         {"web": (1, "npm ERR! something")}, [entry(ghsa_a)],
         "web: npm audit output is not JSON", {}),
        ("an npm error object fails closed",
         {"web": (1, json.dumps({"error": {"code": "ENOLOCK", "summary": "no lockfile"}}))},
         [], "no lockfile", {}),
        ("npm exiting 1 with nothing at high fails closed",
         {"web": (1, clean)}, [], "failed for a reason other than findings", {}),
        ("npm exiting with another status fails closed",
         {"web": (2, clean)}, [], "exited 2", {}),
        ("npm exiting 0 over a high finding fails closed",
         {"web": (0, picomatch)}, [entry(ghsa_a)], "exited 0 but the report lists",
         {"web": dev_lock}),
        ("npm not runnable fails closed",
         {"web": OSError("npm: not found")}, [], "could not run npm audit", {}),
        ("a report of another version fails closed",
         {"web": (0, json.dumps({"auditReportVersion": 1, "vulnerabilities": {}}))},
         [], "auditReportVersion", {}),
        ("an advisory without a GHSA id fails closed",
         {"web": (1, report({"z": ("high", [{**adv(ghsa_b, "high", "z"),
                                              "url": "https://example.com/1"}])}))},
         [], "no GHSA id", {}),
        ("a package without lockfile nodes fails closed",
         {"web": (1, picomatch.replace('"nodes": ["node_modules/picomatch"]', '"nodes": []'))},
         [entry(ghsa_a)], "has no readable list of lockfile nodes", {"web": dev_lock}),
        ("a transitive edge to a package the report omits fails closed",
         {"web": (1, report({"vite": ("high", ["picomatch"])}))}, [],
         "which the report does not list", {}),
        ("a high package no high advisory explains fails closed",
         {"web": (1, report({"q": ("high", [adv(ghsa_b, "low", "q")])}))}, [],
         "no high/critical advisory in the report explains it", {}),
        ("npm's counts disagreeing with the packages listed fails closed",
         {"web": (1, picomatch.replace('"high": 3', '"high": 1'))}, [entry(ghsa_a)],
         "npm counts", {"web": dev_lock}),
        ("an allowlist that is not JSON fails closed",
         {"web": (1, picomatch)}, "not json", "allowlist: not JSON", {"web": dev_lock}),
    ]

    failed = 0
    with tempfile.TemporaryDirectory() as tmp:
        for name, trees, allow, must, locks in cases:
            if isinstance(allow, Path):
                path = allow
            else:
                path = Path(tmp) / "allow.json"
                path.write_text(allow if isinstance(allow, str) else json.dumps(allow))

            def run(tree, trees=trees):
                result = trees[tree]
                if isinstance(result, Exception):
                    raise result
                return result

            def read_lock(tree, locks=locks):
                if tree not in locks:
                    raise FileNotFoundError(f"{tree}/package-lock.json")
                return locks[tree]

            code, out = gate(list(trees), path, now, run, read_lock)
            if must is None:
                ok = code == 0 and "FAIL" not in out
            else:
                ok = code != 0 and must in out
            print(f"{'ok  ' if ok else 'FAIL'} {name}")
            if not ok:
                failed += 1
                print("     " + out.replace("\n", "\n     "))

        extra = 0

        # The command line is the threshold. --omit=dev or a lower --audit-level
        # here would pass every case above, because each feeds the gate a report
        # instead of running npm.
        extra += 1
        ok = NPM_ARGV == ("npm", "audit", "--json", "--audit-level=high")
        print(f"{'ok  ' if ok else 'FAIL'} npm audit runs over the whole tree at high")
        if not ok:
            failed += 1
            print(f"     NPM_ARGV is {NPM_ARGV!r}")

        # The committed allowlist has to be one this gate can read. Its expiry is
        # deliberately not checked here: the live gate does that against the clock.
        extra += 1
        _, _, problems = read_allowlist(ALLOWLIST.read_text(), datetime.min.replace(
            tzinfo=timezone.utc))
        shape = [p for p in problems if "more than" not in p]
        ok = not shape
        print(f"{'ok  ' if ok else 'FAIL'} the committed allowlist parses")
        if not ok:
            failed += 1
            print("     " + "\n     ".join(shape))

    total = len(cases) + extra
    print(f"\n{total - failed}/{total} selftest cases passed")
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
