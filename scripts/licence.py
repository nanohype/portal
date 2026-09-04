#!/usr/bin/env python3
"""Assert that every artifact in this tree declares the licence the tree grants.

The repository states its licence in more than one artifact — grant files, a
prose section, package manifests, image labels. Each is edited on its own, so
agreement between them holds only for as long as something checks it.

WHAT THIS CHECKS

    The POPULATION is derived. It walks every file git knows about, tracked plus
    untracked-but-not-ignored, and classifies each by kind. A manifest added
    anywhere is in scope the moment it exists.

    A derived walk is not enough on its own. Every reading stacked on top of it
    — which basenames count, which heading a README section carries, which table
    a TOML key sits in, which spellings a name table knows — is another place an
    artifact can leave the population, and a walk followed by a silent reading is
    a list wearing a costume. So the readings here are wide, and where one cannot
    read an artifact it says so and fails rather than dropping it: an artifact
    that looks like it declares something and cannot be read is reported, not
    skipped.

    The PROPERTY is that the grants define what this tree offers, and every other
    declaration names a licence the grants make. Searching for a licence's name
    would pass a file saying it is NOT under that licence and would object to
    nothing when a second licence appeared beside the first.

    GRANT IDENTITY IS READ FROM OPERATIVE TEXT — the words that do the granting,
    never the title. A file keeping one licence's heading over another's body is
    the case that matters, because the grants are what every other declaration is
    measured against: the one artifact defining the correct answer must not be the
    one taken on trust. A grant whose text matches no operative signature, or more
    than one, fails rather than being guessed at.

WHAT THIS DOES NOT CHECK, stated so the claim is not read wider than it holds

    - Per-file SPDX headers in source files. This tree has none; a repository
      that adopts them needs a kind added here.
    - The licences of dependencies. Vendored trees are excluded by path
      (VENDORED_DIRS): node_modules, vendor, third_party and the rest. A
      dependency vendored outside those directories is read as this project's
      own, and would be reported as a disagreement — the exclusion is by
      convention, not by knowing what is a dependency.
    - Licence compatibility, and whether the licence is appropriate. Artifacts
      that agree are consistent, which is neither.
    - Whether a licence *expression* is well-formed beyond the AND/OR/WITH forms
      read below. An expression this gate cannot parse fails and says so.

Exit 0 when every declaration names a licence the grants make, 1 otherwise,
naming the file. `--print` lists every artifact and what it declares.
`selftest` runs the gate against trees carrying the drift it exists to catch.
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

# ── the vocabulary ─────────────────────────────────────────────────────────

SPDX_ALIASES: dict[str, str] = {
    "apache-2.0": "Apache-2.0",
    "apache 2.0": "Apache-2.0",
    "apache2": "Apache-2.0",
    "apache-2": "Apache-2.0",
    "asl 2.0": "Apache-2.0",
    "asl-2.0": "Apache-2.0",
    "apache license 2.0": "Apache-2.0",
    "apache license, version 2.0": "Apache-2.0",
    "apache license version 2.0": "Apache-2.0",
    "the apache license, version 2.0": "Apache-2.0",
    "apache software license 2.0": "Apache-2.0",
    "mit": "MIT",
    "mit license": "MIT",
    "the mit license": "MIT",
    "expat": "MIT",
    "bsd-2-clause": "BSD-2-Clause",
    "bsd 2-clause": "BSD-2-Clause",
    "bsd-3-clause": "BSD-3-Clause",
    "bsd 3-clause": "BSD-3-Clause",
    "isc": "ISC",
    "isc license": "ISC",
    "mpl-2.0": "MPL-2.0",
    "mozilla public license 2.0": "MPL-2.0",
    "gpl-3.0": "GPL-3.0",
    "gpl-3.0-only": "GPL-3.0",
    "gpl-3.0-or-later": "GPL-3.0",
    "gnu general public license v3.0": "GPL-3.0",
    "agpl-3.0": "AGPL-3.0",
    "agpl-3.0-only": "AGPL-3.0",
    "agpl-3.0-or-later": "AGPL-3.0",
    "lgpl-3.0": "LGPL-3.0",
    "unlicense": "Unlicense",
    "proprietary": "Proprietary",
}

# Operative signatures. Each `requires` phrase is drawn from the words that do
# the granting, so a file that names a licence in its heading and grants
# something else does not match it. `absent` carries the other half: where one
# licence's operative text is a superset of another's, the subset's signature has
# to exclude the clause that separates them. AGPLv3 contains GPLv3's section 10
# verbatim and adds section 13, so a GPL signature with no exclusion matches an
# AGPL grant and the two are told apart by list order instead of by their text.
GRANT_SIGNATURES: list[tuple[str, tuple[str, ...], tuple[str, ...]]] = [
    ("Apache-2.0",
     ("each contributor hereby grants to you a perpetual",
      "irrevocable copyright license to reproduce"), ()),
    ("MIT",
     ("permission is hereby granted, free of charge, to any person obtaining a copy",
      "to deal in the software without restriction"), ()),
    ("ISC",
     ("permission to use, copy, modify, and/or distribute this software for any purpose",), ()),
    ("BSD-3-Clause",
     ("redistribution and use in source and binary forms, with or without",
      "neither the name of"), ()),
    ("BSD-2-Clause",
     ("redistribution and use in source and binary forms, with or without",),
     ("neither the name of",)),
    ("MPL-2.0",
     ("each contributor hereby grants you a world-wide, royalty-free, non-exclusive license",), ()),
    ("AGPL-3.0",
     ("interacting with it remotely through a computer network",
      "opportunity to receive the corresponding source"), ()),
    ("GPL-3.0",
     ("the recipient automatically receives a license from the original licensors",
      "to run, modify and propagate that work"),
     ("interacting with it remotely through a computer network",)),
    ("Unlicense",
     ("this is free and unencumbered software released into the public domain",), ()),
]

# Vendored trees. A dependency's own grant is not this project's declaration,
# and the scope note above says dependency licences are not checked — so they
# are kept out of the population rather than read and then explained away.
VENDORED_DIRS = {
    "node_modules", "vendor", "third_party", "thirdparty", "external",
    "dist", "build", ".venv", "venv", "site-packages",
}

# Fixture trees exist to carry deliberately wrong content.
FIXTURE_DIRS = {"testdata", "fixtures"}

# A grant file is named for the grant and carries prose, so the tag that
# distinguishes one grant from another (LICENSE-MIT) is allowed and a source
# extension is not — this file is called licence.py and quotes several licences'
# operative wording, which a looser pattern reads as a grant of all of them.
DOC_SUFFIX = r"(\.(md|txt|rst))?"
GRANT_NAMES = re.compile(
    r"^(LICEN[CS]E|COPYING|NOTICE|COPYRIGHT)([-_][A-Za-z0-9.+-]+)?" + DOC_SUFFIX + r"$",
    re.IGNORECASE)
README_NAMES = re.compile(r"^README([-_][A-Za-z0-9.+-]+)?" + DOC_SUFFIX + r"$", re.IGNORECASE)
DOCKERFILE_NAMES = re.compile(r"^(Dockerfile|Containerfile)([-._].*)?$", re.IGNORECASE)

# npm's documented way to defer to a grant file, and its marker for a package
# that publishes no grant at all. Neither is a competing declaration.
NPM_DEFER = re.compile(r"^see\s+licen[cs]e\s+in\s+.+$", re.IGNORECASE)
NPM_UNLICENSED = "unlicensed"


def spdx(raw: str) -> str | None:
    key = " ".join(raw.strip().strip(".,;:").split()).lower()
    return SPDX_ALIASES.get(key)


class Declaration:
    """What one artifact says.

    `licences` is the set an expression names — a single licence for the common
    case, several for a dual grant. `defers` marks an artifact that points at the
    grant rather than restating it, which is a correct thing to do and not a
    declaration to compare. `note` is set when the artifact looks like it
    declares something this gate could not read, which fails rather than passing
    quietly.
    """

    def __init__(self, path: str, kind: str, licences: set[str] | None = None,
                 defers: bool = False, note: str = ""):
        self.path = path
        self.kind = kind
        self.licences = licences or set()
        self.defers = defers
        self.note = note

    def describe(self) -> str:
        if self.defers:
            return "defers to the grant"
        if self.licences:
            return " OR ".join(sorted(self.licences))
        return "—"


# ── SPDX expressions ───────────────────────────────────────────────────────

def parse_expression(raw: str) -> tuple[set[str], str]:
    """Read an SPDX licence expression into the set of licences it names.

    Handles the forms a manifest actually carries: a bare identifier, `A OR B`,
    `A AND B`, and `A WITH exception`. An exception narrows a licence rather than
    naming another, so the licence it qualifies is what the expression names.
    Anything else is refused with a message saying so, because a licence field
    this gate cannot read is a declaration nobody is checking.
    """
    text = raw.strip()
    if not text:
        return set(), "is empty"

    # An exception qualifies the licence to its left.
    parts = re.split(r"\s+(?:OR|AND)\s+", text, flags=re.IGNORECASE)
    licences: set[str] = set()
    for part in parts:
        base = re.split(r"\s+WITH\s+", part, flags=re.IGNORECASE)[0]
        base = base.strip().strip("()").strip()
        identity = spdx(base)
        if identity is None:
            return set(), (
                f"names {base!r}, which this gate does not recognise as a licence. "
                f"Spell it as an SPDX identifier (for example Apache-2.0), or add "
                f"the spelling to SPDX_ALIASES in {Path(__file__).name}"
            )
        licences.add(identity)
    return licences, ""


# ── readers, one per kind ──────────────────────────────────────────────────

def read_grant(path: Path) -> Declaration:
    """Identify a grant from its operative text.

    Every signature is tried and all matches collected. More than one match is a
    failure rather than a first-wins guess: two licences' operative text should
    not both be present, and if they are, what the file grants is not something
    this gate should decide.
    """
    body = " ".join(path.read_text(encoding="utf-8", errors="replace").lower().split())
    matched = [
        identity for identity, requires, absent in GRANT_SIGNATURES
        if all(p in body for p in requires) and not any(p in body for p in absent)
    ]
    if len(matched) == 1:
        return Declaration(str(path), "licence grant", {matched[0]})
    if len(matched) > 1:
        return Declaration(str(path), "licence grant", note=(
            f"carries the operative text of more than one licence ({', '.join(sorted(matched))}), "
            "so what it grants is not determinable"))
    return Declaration(str(path), "licence grant", note=(
        "grants nothing this gate can identify: no licence's operative wording is in it. "
        "A heading naming a licence is not a grant"))


def read_npm(path: Path) -> Declaration:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        return Declaration(str(path), "npm manifest", note=f"is not readable as JSON: {exc}")
    if not isinstance(data, dict):
        return Declaration(str(path), "npm manifest", note="is not a JSON object")

    raw = data.get("license")
    if isinstance(raw, dict):
        raw = raw.get("type")
    if raw is None and isinstance(data.get("licenses"), list):
        names = [e.get("type") for e in data["licenses"] if isinstance(e, dict)]
        raw = " OR ".join(n for n in names if isinstance(n, str)) or None
    if not isinstance(raw, str) or not raw.strip():
        return Declaration(str(path), "npm manifest")

    if NPM_DEFER.match(raw.strip()):
        return Declaration(str(path), "npm manifest", defers=True)
    if raw.strip().lower() == NPM_UNLICENSED:
        # npm's marker for a package that publishes no grant of its own. A
        # private package inside a licensed repository is the ordinary case.
        return Declaration(str(path), "npm manifest", defers=True)

    licences, problem = parse_expression(raw)
    if problem:
        return Declaration(str(path), "npm manifest", note=f"its license field {problem}")
    return Declaration(str(path), "npm manifest", licences)


def _toml_licence(path: Path) -> tuple[str | None, bool]:
    """Any `license` declaration in a TOML manifest, and whether it defers.

    The key is looked for anywhere in the file rather than under one table name.
    Cargo puts it under [package] and [workspace.package]; Poetry under
    [tool.poetry]; PEP 621 under [project]. A gate that knows one of those reads
    the others as declaring nothing, which is the silent exit this avoids.
    """
    body = path.read_text(encoding="utf-8", errors="replace")
    if re.search(r"^\s*license\s*\.\s*workspace\s*=\s*true", body, re.MULTILINE):
        return None, True                      # inherits the workspace's
    if re.search(r'^\s*license[-_]file\s*=', body, re.MULTILINE):
        return None, True                      # points at a grant file
    m = re.search(r'^\s*license\s*=\s*"([^"]*)"', body, re.MULTILINE)
    if m:
        return m.group(1), False
    m = re.search(r'^\s*license\s*=\s*\{[^}]*\btext\s*=\s*"([^"]*)"', body, re.MULTILINE)
    if m:
        return m.group(1), False
    if re.search(r'^\s*license\s*=\s*\{[^}]*\bfile\s*=', body, re.MULTILINE):
        return None, True                      # points at a grant file
    return None, False


def read_toml_manifest(kind: str):
    def reader(path: Path) -> Declaration:
        raw, defers = _toml_licence(path)
        if defers:
            return Declaration(str(path), kind, defers=True)
        if raw is None:
            return Declaration(str(path), kind)
        licences, problem = parse_expression(raw)
        if problem:
            return Declaration(str(path), kind, note=f"its license key {problem}")
        return Declaration(str(path), kind, licences)
    return reader


def read_helm(path: Path) -> Declaration:
    body = path.read_text(encoding="utf-8", errors="replace")
    m = re.search(r'^\s*artifacthub\.io/license:\s*"?([^"\n]+)"?\s*$', body, re.MULTILINE)
    if not m:
        return Declaration(str(path), "helm chart")
    licences, problem = parse_expression(m.group(1))
    if problem:
        return Declaration(str(path), "helm chart", note=f"its licence annotation {problem}")
    return Declaration(str(path), "helm chart", licences)


def read_image(path: Path) -> Declaration:
    """The OCI licences label, which is where an image states its licence."""
    body = path.read_text(encoding="utf-8", errors="replace")
    m = re.search(r'org\.opencontainers\.image\.licenses\s*=\s*"?([^"\n\\]+)"?',
                  body, re.IGNORECASE)
    if not m:
        return Declaration(str(path), "image label")
    licences, problem = parse_expression(m.group(1))
    if problem:
        return Declaration(str(path), "image label", note=f"its licences label {problem}")
    return Declaration(str(path), "image label", licences)


# A heading naming the licence, in the forms markdown actually carries: ATX with
# any level, setext underlined, and a bolded line. Case-insensitive, and matching
# "Licensing" and "License and Attribution" as well as the bare word — a section
# leaving the population because it was retitled is the silent exit again.
README_SECTION = re.compile(
    r"""(?:
          ^\#{1,6}[^\n]*?licen[cs](?:e|ing)[^\n]*$      # ## License, ### Licensing
        | ^[^\n]*?licen[cs](?:e|ing)[^\n]*\n[=-]{3,}$   # setext underline
        | ^\s*\*\*[^\n]*?licen[cs](?:e|ing)[^\n]*\*\*\s*$
        )""",
    re.IGNORECASE | re.MULTILINE | re.VERBOSE,
)
GRANT_REFERENCE = re.compile(r"\b(LICEN[CS]E|COPYING|NOTICE)([-._][A-Za-z0-9.-]+)?\b")


def read_readme(path: Path) -> Declaration:
    """The licence a README's licence section names.

    Only the licence section is read: a licence named elsewhere in prose is a
    mention, not a grant. Within it, every recognised name is collected. Naming
    exactly one is a declaration; naming several fails, which is also what a
    sentence saying the project is under one licence and not another produces.

    A section that only points at the grant file defers. A section that names
    nothing this gate recognises is reported rather than skipped — a spelling
    outside the table is exactly how an artifact leaves the population quietly.
    """
    body = path.read_text(encoding="utf-8", errors="replace")

    match = README_SECTION.search(body)
    if not match:
        # No section. If the file names a licence anywhere, say so rather than
        # dropping the artifact: a retitled section reads exactly like this.
        loose = {SPDX_ALIASES[s] for s in SPDX_ALIASES
                 if re.search(rf"(?<![a-z0-9]){re.escape(s)}(?![a-z0-9])", body, re.IGNORECASE)}
        if loose:
            return Declaration(str(path), "readme", note=(
                f"names {', '.join(sorted(loose))} but has no licence section this gate found, "
                "so what it declares is not readable. Give the section a heading containing "
                '"licence" or "license"'))
        return Declaration(str(path), "readme")

    scope = body[match.end():]
    nxt = re.search(r"^\#{1,6}\s", scope, re.MULTILINE)
    if nxt:
        scope = scope[:nxt.start()]

    found = {SPDX_ALIASES[s] for s in SPDX_ALIASES
             if re.search(rf"(?<![a-z0-9]){re.escape(s)}(?![a-z0-9])", scope, re.IGNORECASE)}
    if len(found) > 1:
        return Declaration(str(path), "readme", note=(
            f"its licence section names more than one licence ({', '.join(sorted(found))}), "
            "so what it grants is not determinable"))
    if found:
        return Declaration(str(path), "readme", found)
    if GRANT_REFERENCE.search(scope):
        return Declaration(str(path), "readme", defers=True)
    return Declaration(str(path), "readme", note=(
        "has a licence section that names no licence this gate recognises and points at no "
        "grant file, so what it declares is not readable"))


KINDS: dict[str, object] = {
    "grant": read_grant,
    "npm": read_npm,
    "cargo": read_toml_manifest("cargo manifest"),
    "pyproject": read_toml_manifest("python manifest"),
    "helm": read_helm,
    "image": read_image,
    "readme": read_readme,
}


def classify(path: Path) -> str | None:
    name = path.name
    if GRANT_NAMES.match(name) or any(p.upper() == "LICENSES" for p in path.parts[:-1]):
        return "grant"
    if name == "package.json":
        return "npm"
    if name == "Cargo.toml":
        return "cargo"
    if name == "pyproject.toml":
        return "pyproject"
    if name == "Chart.yaml":
        return "helm"
    if DOCKERFILE_NAMES.match(name):
        return "image"
    if README_NAMES.match(name):
        return "readme"
    return None


def excluded(path: Path) -> bool:
    parts = set(path.parts[:-1])
    return bool(parts & VENDORED_DIRS) or bool(parts & FIXTURE_DIRS)


def tree_files() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        capture_output=True, text=True, check=True,
    ).stdout
    return [Path(p) for p in out.split("\0") if p]


def collect() -> tuple[list[Declaration], list[Declaration]]:
    declarations: list[Declaration] = []
    unreadable: list[Declaration] = []
    for path in tree_files():
        if excluded(path) or not path.is_file():
            continue
        kind = classify(path)
        if kind is None:
            continue
        d = KINDS[kind](path)
        if d.note:
            unreadable.append(d)
        elif d.licences or d.defers:
            declarations.append(d)
    return declarations, unreadable


def main() -> int:
    if "selftest" in sys.argv:
        return selftest()
    show = "--print" in sys.argv

    declarations, unreadable = collect()
    grants = [d for d in declarations if d.kind == "licence grant"]
    others = [d for d in declarations if d.kind != "licence grant" and not d.defers]

    if show:
        for d in sorted(declarations + unreadable, key=lambda d: d.path):
            mark = d.describe() if not d.note else "?"
            print(f"  {mark:<24} {d.kind:<14} {d.path}" + (f" — {d.note}" if d.note else ""))

    failures: list[str] = []
    for d in unreadable:
        failures.append(f"{d.path}: {d.note}")

    offered: set[str] = set()
    for g in grants:
        offered |= g.licences

    if not offered:
        failures.append(
            "no licence grant in this tree: nothing states what the project offers, so nothing "
            "can be checked against it. Add a LICENSE file carrying a licence's own text.")

    for d in others:
        outside = d.licences - offered
        if offered and outside:
            failures.append(
                f"{d.path} ({d.kind}) declares {d.describe()}, and this tree grants "
                f"{' and '.join(sorted(offered))}. "
                f"{', '.join(sorted(outside))} is granted by no file here.")

    # More than one grant is the dual-licence layout, which is a real thing. What
    # it cannot be is silent: something has to say which of them applies, or a
    # second grant added beside the first changes what the project offers with
    # nothing recording the choice.
    if len(offered) > 1:
        expressed = any(d.licences == offered for d in others)
        if not expressed:
            carried = ", ".join(f"{g.path} ({g.describe()})"
                                for g in sorted(grants, key=lambda g: g.path))
            failures.append(
                f"this tree carries more than one grant — {carried} — and nothing says which "
                "applies. A dual grant has to be declared somewhere a consumer reads: a package "
                f"manifest or the README, spelled \"{' OR '.join(sorted(offered))}\".")

    if failures:
        for f in failures:
            print(f"::error::{f}")
        print("== licence declarations do not agree with what this tree grants ==")
        return 1

    print(f"== every licence declaration in this tree is {' OR '.join(sorted(offered))} "
          f"({len(declarations)} artifacts) ==")
    return 0


# ── the gate, watched to reject ────────────────────────────────────────────

def selftest() -> int:
    """Run the gate against trees carrying the drift it exists to catch.

    Each case plants one change in a clone of this tree and requires a non-zero
    exit AND the offending file named — an exit code alone is satisfied by a gate
    that refuses everything. The accepting cases are the other half: correct work
    that must not red-build, because a gate that fails correct work is switched
    off and then protects nothing.
    """
    import shutil
    import tempfile

    root = Path(__file__).resolve().parent.parent
    here = Path(__file__).resolve()

    def write(rel: str, text: str):
        return lambda t: ((t / rel).parent.mkdir(parents=True, exist_ok=True),
                          (t / rel).write_text(text))

    def edit(rel: str, old: str, new: str):
        return lambda t: (t / rel).write_text((t / rel).read_text().replace(old, new))

    apache_body = (root / "LICENSE").read_text()
    mit_body = ("MIT License\n\nPermission is hereby granted, free of charge, to any person "
                "obtaining a copy of this software and associated documentation files (the "
                "\"Software\"), to deal in the Software without restriction.\n")

    # GPLv3 section 10, verbatim. AGPLv3 carries the same paragraph and adds
    # section 13, so this pair differs only by the clause that separates the two
    # licences and nothing else a signature could key on.
    gpl_section_10 = (
        "Each time you convey a covered work, the recipient automatically receives a "
        "license from the original licensors, to run, modify and propagate that work, "
        "subject to this License.\n")
    agpl_section_13 = (
        "Notwithstanding any other provision of this License, if you modify the Program, "
        "your modified version must prominently offer all users interacting with it "
        "remotely through a computer network (if your version supports such interaction) "
        "an opportunity to receive the Corresponding Source of your version by providing "
        "access to the Corresponding Source from a network server at no charge.\n")
    gpl_body = "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n\n" + gpl_section_10
    agpl_body = ("GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007\n\n"
                 + gpl_section_10 + "\n" + agpl_section_13)

    # (name, plant, must-name). Non-zero exit required.
    rejecting = [
        ("the README names a licence the grant does not make",
         edit("README.md", "[Apache License 2.0](LICENSE).", "[MIT License](LICENSE)."),
         "README.md"),
        ("a package manifest declares a second licence",
         lambda t: (t / "web" / "package.json").write_text(json.dumps(
             {**json.loads((t / "web" / "package.json").read_text()), "license": "MIT"},
             indent=2) + "\n"),
         "web/package.json"),
        ("a manifest this gate was never told about declares a third",
         write("tools/newthing/package.json",
               json.dumps({"name": "newthing", "license": "GPL-3.0"}, indent=2) + "\n"),
         "tools/newthing/package.json"),
        ("the licence section names one licence while denying another",
         edit("README.md", "[Apache License 2.0](LICENSE).",
              "This project is not under the Apache License 2.0. It is under the MIT License."),
         "README.md"),

        # The blocker: identity has to come from operative text.
        ("the grant keeps its Apache heading over an MIT body",
         write("LICENSE", "\n".join(apache_body.splitlines()[:3]) + "\n\n" + mit_body),
         "README.md"),
        ("the grant is cut down to a heading and grants nothing",
         write("LICENSE", "\n".join(apache_body.splitlines()[:3]) + "\n"),
         "LICENSE"),

        # The readings stacked on the walk.
        ("a second grant file appears with nothing declaring the choice",
         write("LICENSE-MIT", mit_body),
         "LICENSE-MIT"),
        ("an image label names a licence the grant does not make",
         edit("docker/Dockerfile.server", "FROM",
              'LABEL org.opencontainers.image.licenses="MIT"\nFROM'),
         "docker/Dockerfile.server"),
        ("a Cargo manifest declares another under the workspace table",
         write("tools/rs/Cargo.toml", '[workspace.package]\nlicense = "MIT"\n'),
         "tools/rs/Cargo.toml"),
        ("a Poetry manifest declares another under its own table",
         write("tools/py/pyproject.toml", '[tool.poetry]\nname = "x"\nlicense = "MIT"\n'),
         "tools/py/pyproject.toml"),
        ("a chart annotation names another",
         write("deploy/helm/other/Chart.yaml",
               'apiVersion: v2\nname: other\nversion: 0.1.0\n'
               'annotations:\n  artifacthub.io/license: MIT\n'),
         "deploy/helm/other/Chart.yaml"),
        ("the README licence section is retitled and its declaration lost",
         edit("README.md", "## License", "## Legal"),
         "README.md"),
        ("the grant carries two licences' operative text at once",
         write("LICENSE", apache_body + "\n" + mit_body),
         "LICENSE"),
        ("a grant is swapped for one that keeps the network clause",
         lambda t: (write("LICENSE", agpl_body)(t),
                    edit("README.md", "[Apache License 2.0](LICENSE).",
                         "[GNU General Public License v3.0](LICENSE).")(t)),
         "README.md"),
        ("a licence field is spelled in a way this gate cannot read",
         lambda t: (t / "web" / "package.json").write_text(json.dumps(
             {**json.loads((t / "web" / "package.json").read_text()),
              "license": "Whatever-1.0"}, indent=2) + "\n"),
         "web/package.json"),
    ]

    # Correct work that must not red-build.
    accepting = [
        ("npm's documented form for deferring to the grant file",
         lambda t: (t / "web" / "package.json").write_text(json.dumps(
             {**json.loads((t / "web" / "package.json").read_text()),
              "license": "SEE LICENSE IN LICENSE"}, indent=2) + "\n")),
        ("a private package marked UNLICENSED",
         lambda t: (t / "web" / "package.json").write_text(json.dumps(
             {**json.loads((t / "web" / "package.json").read_text()),
              "license": "UNLICENSED"}, indent=2) + "\n")),
        ("this tree's own licence with an exception",
         lambda t: (t / "web" / "package.json").write_text(json.dumps(
             {**json.loads((t / "web" / "package.json").read_text()),
              "license": "Apache-2.0 WITH LLVM-exception"}, indent=2) + "\n")),
        ("a vendored dependency carrying its own MIT grant",
         write("web/node_modules/left-pad/LICENSE", mit_body)),
        ("a dependency vendored under vendor/",
         write("vendor/example.com/dep/LICENSE", mit_body)),
        ("a Cargo manifest inheriting the workspace licence",
         write("tools/rs/Cargo.toml", '[package]\nname = "x"\nlicense.workspace = true\n')),
        ("a pyproject pointing at the grant file",
         write("tools/py/pyproject.toml", '[project]\nname = "x"\nlicense = {file = "LICENSE"}\n')),
        ("the README's section spelled LICENSE in capitals",
         edit("README.md", "## License", "## LICENSE")),
        ("the README's section retitled License and Attribution",
         edit("README.md", "## License", "## License and Attribution")),
        ("a dual grant declared as an expression",
         lambda t: (write("LICENSE-MIT", mit_body)(t),
                    (t / "web" / "package.json").write_text(json.dumps(
                        {**json.loads((t / "web" / "package.json").read_text()),
                         "license": "Apache-2.0 OR MIT"}, indent=2) + "\n"))),
        ("a grant carrying the network clause is read as AGPL, not GPL",
         lambda t: (write("LICENSE", agpl_body)(t),
                    edit("README.md", "[Apache License 2.0](LICENSE).",
                         "[AGPL-3.0](LICENSE).")(t))),
        ("the same grant without that clause is read as GPL, not AGPL",
         lambda t: (write("LICENSE", gpl_body)(t),
                    edit("README.md", "[Apache License 2.0](LICENSE).",
                         "[GPL-3.0](LICENSE).")(t))),
        ("an image label naming this tree's own licence",
         edit("docker/Dockerfile.server", "FROM",
              'LABEL org.opencontainers.image.licenses="Apache-2.0"\nFROM')),
    ]

    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        def clone(tag: str) -> Path:
            tree = Path(tmp) / re.sub(r"[^a-z0-9]+", "-", tag.lower())[:48]
            subprocess.run(["git", "clone", "--quiet", "--depth", "1", "--no-hardlinks",
                            str(root), str(tree)], check=True, capture_output=True)
            shutil.copy(here, tree / "scripts" / here.name)
            return tree

        def run(tree: Path):
            return subprocess.run([sys.executable, str(tree / "scripts" / here.name)],
                                  capture_output=True, text=True, cwd=tree)

        print("  rejects:")
        for name, plant, must_name in rejecting:
            tree = clone("r-" + name)
            plant(tree)
            r = run(tree)
            ok = r.returncode != 0 and must_name in r.stdout
            print(f"    {'ok  ' if ok else 'FAIL'}  {name}")
            if not ok:
                failures += 1
                print(f"          exit {r.returncode}, wanted non-zero naming {must_name}")
                print("          " + r.stdout.strip().replace("\n", "\n          "))

        print("  accepts:")
        for name, plant in accepting:
            tree = clone("a-" + name)
            plant(tree)
            r = run(tree)
            ok = r.returncode == 0
            print(f"    {'ok  ' if ok else 'FAIL'}  {name}")
            if not ok:
                failures += 1
                print("          " + r.stdout.strip().replace("\n", "\n          "))

        tree = clone("control")
        r = run(tree)
        ok = r.returncode == 0
        print(f"  {'ok  ' if ok else 'FAIL'}  the tree as it stands agrees with itself")
        if not ok:
            failures += 1
            print("        " + r.stdout.strip())

    if failures:
        print(f"== {failures} licence assertion(s) do not hold ==")
        return 1
    print(f"== every licence assertion holds ({len(rejecting)} rejects, "
          f"{len(accepting)} accepts, 1 control) ==")
    return 0


if __name__ == "__main__":
    sys.exit(main())
