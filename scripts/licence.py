#!/usr/bin/env python3
"""Assert that every artifact in this tree declares the same licence.

The repository states its licence in more than one place, and the places drift
independently: a grant file, a prose section that names it, and any package
manifest with a licence field. Nothing made them agree, so a README naming one
licence over a grant file making another is a change nobody's tooling objects to.

WHAT THIS CHECKS
    Two things, and it is worth separating them.

    The POPULATION is derived, not listed. It walks every file git knows about
    — tracked, plus untracked files git would not ignore — and classifies each
    by kind. A manifest added anywhere in the tree is therefore in scope the
    moment it exists, without this file being edited. A gate whose population is
    a list of names it was written with is correct on the day it is written and
    silently narrower every day after.

    The PROPERTY is agreement between declarations, not the presence of a word.
    Each artifact is asked which licence it declares and the answers must match.
    Searching for the name of the licence would pass a file that says it is NOT
    under that licence, and would fail nothing at all when a second licence
    appears alongside the first.

    The grant file's own licence is identified from its text, by matching the
    signature phrases each licence carries. It is not assumed to be the one
    everything else names — the grant is what the project actually offers, so
    it is the thing to read rather than the thing to take on trust.

WHAT THIS DOES NOT CHECK, stated so the claim is not read wider than it is
    - Per-file SPDX headers in source files. This tree has none; a repository
      that adopts them needs a kind added here, and until then this gate says
      nothing about them.
    - The licences of dependencies. That is a different question with a
      different answer, and `npm audit`/`go-licenses` are the tools for it.
    - Licence compatibility. Two artifacts that agree are consistent, which is
      not the same as either being appropriate.

Exit 0 when every declaration agrees, 1 otherwise, naming the file and what it
declares. Run with --print to list every artifact and its declaration.
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

# ── the vocabulary ─────────────────────────────────────────────────────────
#
# Licences are compared by identity, so every spelling of one licence has to
# reduce to the same identifier. The identifiers are SPDX's, which is the naming
# a manifest's `license` field already expects.

SPDX_ALIASES: dict[str, str] = {
    "apache-2.0": "Apache-2.0",
    "apache 2.0": "Apache-2.0",
    "apache license 2.0": "Apache-2.0",
    "apache license, version 2.0": "Apache-2.0",
    "apache license version 2.0": "Apache-2.0",
    "the apache license, version 2.0": "Apache-2.0",
    "mit": "MIT",
    "mit license": "MIT",
    "the mit license": "MIT",
    "bsd-2-clause": "BSD-2-Clause",
    "bsd-3-clause": "BSD-3-Clause",
    "isc": "ISC",
    "isc license": "ISC",
    "mpl-2.0": "MPL-2.0",
    "mozilla public license 2.0": "MPL-2.0",
    "gpl-3.0": "GPL-3.0",
    "gnu general public license v3.0": "GPL-3.0",
    "agpl-3.0": "AGPL-3.0",
    "lgpl-3.0": "LGPL-3.0",
    "unlicense": "Unlicense",
    "proprietary": "Proprietary",
    "unlicensed": "Proprietary",
}

# Signature phrases that identify a grant file's text. Each is distinctive
# enough that a file carrying it is that licence: the phrase is drawn from the
# licence's own operative wording, not from its title, so a file that merely
# mentions a licence by name does not match.
GRANT_SIGNATURES: list[tuple[str, tuple[str, ...]]] = [
    ("Apache-2.0", ("apache license", "version 2.0, january 2004")),
    ("MIT", ("permission is hereby granted, free of charge, to any person obtaining a copy",)),
    ("ISC", ("permission to use, copy, modify, and/or distribute this software",)),
    ("BSD-3-Clause", ("redistribution and use in source and binary forms", "neither the name of")),
    ("BSD-2-Clause", ("redistribution and use in source and binary forms",)),
    ("MPL-2.0", ("mozilla public license version 2.0",)),
    ("AGPL-3.0", ("gnu affero general public license", "version 3")),
    ("GPL-3.0", ("gnu general public license", "version 3")),
    ("Unlicense", ("this is free and unencumbered software released into the public domain",)),
]

GRANT_NAMES = re.compile(r"^(LICEN[CS]E|COPYING)(\.(md|txt|rst))?$", re.IGNORECASE)
README_NAMES = re.compile(r"^README(\.(md|rst|txt))?$", re.IGNORECASE)


def spdx(raw: str) -> str | None:
    """Reduce a spelling to its identifier, or None when it names no licence."""
    key = " ".join(raw.strip().strip(".,;:").split()).lower()
    return SPDX_ALIASES.get(key)


class Declaration:
    def __init__(self, path: str, kind: str, licence: str | None, note: str = ""):
        self.path = path
        self.kind = kind
        self.licence = licence
        self.note = note


# ── readers, one per kind ──────────────────────────────────────────────────


def read_grant(path: Path) -> Declaration:
    """Identify a grant file from its own text."""
    body = path.read_text(encoding="utf-8", errors="replace").lower()
    for identity, phrases in GRANT_SIGNATURES:
        if all(p in body for p in phrases):
            return Declaration(str(path), "licence grant", identity)
    return Declaration(
        str(path), "licence grant", None,
        "its text matches no licence this gate can identify, so nothing can be checked against it",
    )


def read_npm(path: Path) -> Declaration:
    """The `license` field of a package manifest."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        return Declaration(str(path), "npm manifest", None, f"is not readable as JSON: {exc}")
    if not isinstance(data, dict):
        return Declaration(str(path), "npm manifest", None, "is not a JSON object")

    raw = data.get("license")
    if isinstance(raw, dict):  # the deprecated {type, url} form
        raw = raw.get("type")
    if raw is None and isinstance(data.get("licenses"), list):
        names = [e.get("type") for e in data["licenses"] if isinstance(e, dict)]
        raw = names[0] if len(set(names)) == 1 else None
    if not isinstance(raw, str) or not raw.strip():
        return Declaration(str(path), "npm manifest", None)

    identity = spdx(raw)
    if identity is None:
        return Declaration(str(path), "npm manifest", None,
                           f'declares licence {raw!r}, which this gate does not recognise')
    return Declaration(str(path), "npm manifest", identity)


def _toml_licence(path: Path, table: str) -> str | None:
    """The `license` key of a named TOML table, read without a TOML parser.

    tomllib would be better, and this repository has no Python dependencies to
    add it to. The forms accepted are the ones these manifests use in practice:
    a bare string, or a table with a `text` key.
    """
    body = path.read_text(encoding="utf-8", errors="replace")
    section = re.search(
        rf"^\[{re.escape(table)}\]\s*$(.*?)(?=^\[|\Z)", body, re.MULTILINE | re.DOTALL
    )
    if not section:
        return None
    scope = section.group(1)
    m = re.search(r'^\s*license\s*=\s*"([^"]*)"', scope, re.MULTILINE)
    if m:
        return m.group(1)
    m = re.search(r'^\s*license\s*=\s*\{[^}]*text\s*=\s*"([^"]*)"', scope, re.MULTILINE)
    return m.group(1) if m else None


def read_cargo(path: Path) -> Declaration:
    raw = _toml_licence(path, "package")
    if raw is None:
        return Declaration(str(path), "cargo manifest", None)
    identity = spdx(raw)
    if identity is None:
        return Declaration(str(path), "cargo manifest", None,
                           f"declares licence {raw!r}, which this gate does not recognise")
    return Declaration(str(path), "cargo manifest", identity)


def read_pyproject(path: Path) -> Declaration:
    raw = _toml_licence(path, "project")
    if raw is None:
        return Declaration(str(path), "python manifest", None)
    identity = spdx(raw)
    if identity is None:
        return Declaration(str(path), "python manifest", None,
                           f"declares licence {raw!r}, which this gate does not recognise")
    return Declaration(str(path), "python manifest", identity)


def read_helm(path: Path) -> Declaration:
    """A chart's licence annotation, which is where Helm carries one."""
    body = path.read_text(encoding="utf-8", errors="replace")
    m = re.search(r'^\s*artifacthub\.io/license:\s*"?([^"\n]+)"?\s*$', body, re.MULTILINE)
    if not m:
        return Declaration(str(path), "helm chart", None)
    identity = spdx(m.group(1))
    if identity is None:
        return Declaration(str(path), "helm chart", None,
                           f"declares licence {m.group(1)!r}, which this gate does not recognise")
    return Declaration(str(path), "helm chart", identity)


def read_readme(path: Path) -> Declaration:
    """The licence a README's licence section names.

    Prose is read as a declaration in one place only: the section headed
    "License" or "Licence". Within it, every recognised licence name is
    collected. Naming exactly one is a declaration of that licence; naming
    several is a failure in its own right, which is also what a sentence saying
    the project is under one licence and not another produces. The rest of a
    README is not parsed, because a licence named anywhere in prose is a mention
    and not a grant.
    """
    body = path.read_text(encoding="utf-8", errors="replace")
    section = re.search(
        r"^#{1,6}\s*Licen[cs]e\s*$(.*?)(?=^#{1,6}\s|\Z)", body, re.MULTILINE | re.DOTALL
    )
    if not section:
        return Declaration(str(path), "readme", None)

    scope = section.group(1)
    found: set[str] = set()
    for spelling, identity in SPDX_ALIASES.items():
        if re.search(rf"(?<![a-z0-9]){re.escape(spelling)}(?![a-z0-9])", scope, re.IGNORECASE):
            found.add(identity)

    if len(found) > 1:
        return Declaration(str(path), "readme", None,
                           "its licence section names more than one licence "
                           f"({', '.join(sorted(found))}), so what it grants is not determinable")
    if not found:
        return Declaration(str(path), "readme", None)
    return Declaration(str(path), "readme", found.pop())


KINDS: list[tuple[str, callable]] = [
    ("grant", read_grant),
    ("npm", read_npm),
    ("cargo", read_cargo),
    ("pyproject", read_pyproject),
    ("helm", read_helm),
    ("readme", read_readme),
]


def classify(path: Path) -> str | None:
    name = path.name
    if GRANT_NAMES.match(name):
        return "grant"
    if name == "package.json":
        return "npm"
    if name == "Cargo.toml":
        return "cargo"
    if name == "pyproject.toml":
        return "pyproject"
    if name == "Chart.yaml":
        return "helm"
    if README_NAMES.match(name):
        return "readme"
    return None


def tree_files() -> list[Path]:
    """Every file this repository carries, derived from git rather than listed.

    Tracked files plus untracked ones git would not ignore, so a manifest added
    to the tree is in scope before it is committed.
    """
    out = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        capture_output=True, text=True, check=True,
    ).stdout
    return [Path(p) for p in out.split("\0") if p]


def selftest() -> int:
    """Run the gate against trees carrying the drift it exists to catch.

    Showing that the gate passes today is a different question from whether it
    would fail tomorrow, and this answers the second. Each case plants one
    disagreement in a copy of the real tree and requires both a non-zero exit and
    the offending file named — an exit code alone is satisfied by a gate that
    refuses everything, and a gate whose population is a written list finds
    nothing in the third case and exits clean.

    The third case is the one that matters most: it adds a manifest the gate has
    never been told about, in a directory that did not exist, and touches nothing
    else. A population derived from the tree sees it; a population typed into
    this file does not.
    """
    import shutil
    import tempfile

    root = Path(__file__).resolve().parent.parent
    here = Path(__file__).resolve()

    cases: list[tuple[str, callable, str]] = [
        (
            "the README names a licence the grant does not make",
            lambda t: (t / "README.md").write_text(
                (t / "README.md").read_text().replace(
                    "[Apache License 2.0](LICENSE).", "[MIT License](LICENSE).")),
            "README.md",
        ),
        (
            "a package manifest declares a second licence",
            lambda t: (t / "web" / "package.json").write_text(
                json.dumps({**json.loads((t / "web" / "package.json").read_text()),
                            "license": "MIT"}, indent=2) + "\n"),
            "web/package.json",
        ),
        (
            "a manifest this gate was never told about declares a third",
            lambda t: (
                (t / "tools" / "newthing").mkdir(parents=True, exist_ok=True),
                (t / "tools" / "newthing" / "package.json").write_text(
                    json.dumps({"name": "newthing", "license": "GPL-3.0"}, indent=2) + "\n"),
            ),
            "tools/newthing/package.json",
        ),
        (
            "the grant file is a licence nothing else names",
            lambda t: (t / "LICENSE").write_text(
                "MIT License\n\nPermission is hereby granted, free of charge, "
                "to any person obtaining a copy of this software\n"),
            "LICENSE",
        ),
        (
            "the licence section names one licence while denying another",
            lambda t: (t / "README.md").write_text(
                (t / "README.md").read_text().replace(
                    "[Apache License 2.0](LICENSE).",
                    "This project is not under the Apache License 2.0. "
                    "It is released under the MIT License.")),
            "README.md",
        ),
    ]

    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        for name, plant, must_name in cases:
            tree = Path(tmp) / re.sub(r"[^a-z]+", "-", name.lower())[:40]
            subprocess.run(["git", "clone", "--quiet", "--depth", "1", "--no-hardlinks",
                            str(root), str(tree)], check=True, capture_output=True)
            shutil.copy(here, tree / "scripts" / here.name)
            plant(tree)

            r = subprocess.run([sys.executable, str(tree / "scripts" / here.name)],
                               capture_output=True, text=True, cwd=tree)
            ok = r.returncode != 0 and must_name in r.stdout
            print(f"  {'ok  ' if ok else 'FAIL'}  {name}")
            if not ok:
                failures += 1
                print(f"        exit {r.returncode}, wanted non-zero naming {must_name}")
                print("        " + r.stdout.strip().replace("\n", "\n        "))

        # A control: the tree as it stands has to pass, or every case above is
        # satisfied by a gate that refuses everything.
        clean = Path(tmp) / "clean"
        subprocess.run(["git", "clone", "--quiet", "--depth", "1", "--no-hardlinks",
                        str(root), str(clean)], check=True, capture_output=True)
        shutil.copy(here, clean / "scripts" / here.name)
        r = subprocess.run([sys.executable, str(clean / "scripts" / here.name)],
                           capture_output=True, text=True, cwd=clean)
        ok = r.returncode == 0
        print(f"  {'ok  ' if ok else 'FAIL'}  the tree as it stands agrees with itself")
        if not ok:
            failures += 1
            print("        " + r.stdout.strip())

    if failures:
        print(f"== {failures} licence assertion(s) do not catch what they claim to ==")
        return 1
    print(f"== every licence assertion catches what it claims to ({len(cases)} cases + control) ==")
    return 0


def main() -> int:
    if "selftest" in sys.argv:
        return selftest()
    show = "--print" in sys.argv
    readers = dict(KINDS)

    declarations: list[Declaration] = []
    unreadable: list[Declaration] = []
    for path in tree_files():
        kind = classify(path)
        if kind is None or not path.is_file():
            continue
        d = readers[kind](path)
        if d.note:
            unreadable.append(d)
        if d.licence is not None:
            declarations.append(d)

    if show:
        for d in sorted(declarations, key=lambda d: d.path):
            print(f"  {d.licence:<14} {d.kind:<14} {d.path}")
        for d in sorted(unreadable, key=lambda d: d.path):
            print(f"  {'?':<14} {d.kind:<14} {d.path} — {d.note}")

    failures: list[str] = []

    grants = [d for d in declarations if d.kind == "licence grant"]
    if not grants:
        failures.append(
            "no licence grant in this tree: nothing states what the project offers, "
            "so nothing can be checked against it. Add a LICENSE file."
        )

    for d in unreadable:
        failures.append(f"{d.path}: {d.note}")

    identities = {d.licence for d in declarations}
    if len(identities) > 1:
        failures.append(
            "this tree declares more than one licence:\n"
            + "\n".join(
                f"    {d.licence:<14} {d.path} ({d.kind})"
                for d in sorted(declarations, key=lambda d: (d.licence or "", d.path))
            )
            + "\n  Every artifact that names a licence has to name the one the grant file makes."
        )

    if failures:
        for f in failures:
            print(f"::error::{f}")
        print("== licence declarations disagree ==")
        return 1

    only = identities.pop() if identities else "none"
    print(f"== every licence declaration in this tree is {only} ({len(declarations)} artifacts) ==")
    return 0


if __name__ == "__main__":
    sys.exit(main())
