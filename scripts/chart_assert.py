#!/usr/bin/env python3
"""Assertions over a rendered Helm chart that no other gate in CI can reach.

    chart_assert.py serviceaccounts <rendered.yaml>...
    chart_assert.py urlquery <rendered.yaml>...
    chart_assert.py selftest

Both derive the set they check from the rendered documents rather than from a
list of names written beside them. A gate that enumerates its own class can only
see the members its author thought of, and it reports success on the rest — so
the failure it exists to catch arrives as a green build.

Every check fails when it finds nothing to examine. A render that produced no
subjects is a broken gate, not a clean chart, and the two must not print the
same thing.
"""

import os
import re
import sys

import yaml


def _docs(paths):
    for path in paths:
        with open(path) as fh:
            for doc in yaml.safe_load_all(fh):
                if doc:
                    yield path, doc


def _pod_specs(node, trail=()):
    """Yield (trail, mapping) for every pod spec in a document.

    A pod spec is a mapping carrying a `containers` list. That is what the
    Kubernetes API means by one, and it holds for a bare Pod
    (`spec.containers`), for every controller that wraps a pod template
    (`spec.template.spec.containers`), for a CronJob's extra level of nesting,
    and for any workload kind added later. Matching on `kind` instead would
    reproduce the author's list of kinds, which is the thing being avoided.
    """
    if isinstance(node, dict):
        if isinstance(node.get("containers"), list):
            yield trail, node
            return
        for key, value in node.items():
            yield from _pod_specs(value, trail + (key,))
    elif isinstance(node, list):
        for i, value in enumerate(node):
            yield from _pod_specs(value, trail + (str(i),))


def serviceaccounts(paths):
    """Every pod the chart renders runs as the tenant ServiceAccount.

    One identity across the release is what makes a NetworkPolicy or an audit
    query written against that ServiceAccount cover every pod the tenant runs.
    An unattributed pod is a hole in that claim.
    """
    seen, bad = 0, []
    for path, doc in _docs(paths):
        for trail, pod in _pod_specs(doc):
            seen += 1
            if not pod.get("serviceAccountName"):
                name = (doc.get("metadata") or {}).get("name", "<unnamed>")
                where = ".".join(trail) or "spec"
                bad.append(f'{doc.get("kind", "<unknown>")}/{name} ({path}:{where})')

    if not seen:
        sys.exit("chart_assert: rendered no pod specs — the check would pass vacuously")
    if bad:
        sys.exit(
            "::error::these run as `default`, so an audit query or NetworkPolicy written "
            "against the tenant ServiceAccount misses them: " + ", ".join(bad)
        )
    print(f"ok — all {seen} rendered pod specs run as the tenant ServiceAccount")


# A Go-template action, and the leading field reference inside it.
_ACTION = re.compile(r"\{\{(.*?)\}\}", re.S)
_FIELD = re.compile(r"^\s*\.([A-Za-z_][A-Za-z0-9_]*)")


def urlquery(paths):
    """Every value interpolated into a composed URL is percent-encoded.

    What ships here is a template the External Secrets controller renders later,
    so helm never sees the credential and neither does kubeconform. This is the
    one object in the chart whose correctness no other gate can reach.

    RDS draws master passwords from "any printable ASCII except /, ", @, or
    space", which leaves # ? : [ ] in play. `#` is the sharp one: net/url.Parse
    reads it as the fragment delimiter and drops the rest of the string, @host
    included, so the app reports the username as the host.

    The subjects are the interpolations the render actually contains, found by
    walking the rendered templates for URL-shaped values. A new credential, a
    new composed URL or a renamed key is therefore checked on the run that
    introduces it, without anyone adding it to a list here.
    """
    subjects, bad = 0, []
    for path, doc in _docs(paths):
        template = (
            ((doc.get("spec") or {}).get("target") or {}).get("template") or {}
        )
        for key, value in (template.get("data") or {}).items():
            if not isinstance(value, str) or "://" not in value:
                continue
            for action in _ACTION.findall(value):
                field = _FIELD.match(action)
                if not field:
                    continue  # not a field reference — a function call or a literal
                subjects += 1
                if "urlquery" not in action:
                    bad.append(f"{path}: {doc.get('kind')}.{key} interpolates .{field.group(1)} raw")

    if not subjects:
        sys.exit(
            "chart_assert: found no values interpolated into a composed URL — either the "
            "render is missing its ExternalSecret or this check no longer looks where they are"
        )
    if bad:
        sys.exit(
            "::error::a credential reaching a URL without urlquery truncates it silently the "
            "moment the value contains # or ?: " + "; ".join(bad)
        )
    print(f"ok — all {subjects} values interpolated into a composed URL go through urlquery")


def selftest(_paths):
    """Run each check against fixtures holding the defect it exists to catch.

    Showing a check fires is a separate question from what it looks at, and this
    answers the second: each failing fixture carries a subject no list of kinds
    or credential names would contain, so a rewrite back to a name list fails
    here rather than going quiet. Each is paired with a passing fixture holding
    the same unlisted subject done correctly, so a check that refuses everything
    cannot satisfy the pair.
    """
    here = os.path.join(os.path.dirname(os.path.abspath(__file__)), "testdata", "chart_assert")
    # Each rejecting case names what the message must contain. Exit status alone
    # is not enough: a check narrowed back to a list of kinds finds nothing in a
    # fixture built from kinds it does not know, and exits on its own vacuity
    # guard — non-zero, for the wrong reason, looking exactly like a catch.
    cases = [
        (serviceaccounts, "unattributed-pods.yaml", "Pod/portal-debug",
         "a bare Pod with no serviceAccountName, beside an attributed Deployment"),
        (serviceaccounts, "unattributed-pods.yaml", "ReplicationController/portal-legacy",
         "a ReplicationController with no serviceAccountName"),
        (serviceaccounts, "attributed-pods.yaml", None,
         "a bare Pod and a CronJob, both attributed"),
        (urlquery, "raw-credential.yaml", ".pgReplicaUser",
         "a credential in a composed URL with no urlquery"),
        (urlquery, "raw-credential.yaml", ".pgReplicaPassword",
         "the second raw credential in the same URL"),
        (urlquery, "escaped-credentials.yaml", None,
         "the same unlisted credentials, escaped"),
    ]
    failures = []
    for check, fixture, want_in_message, what in cases:
        path = os.path.join(here, fixture)
        message = None
        try:
            check([path])
        except SystemExit as exc:
            message = str(exc.code) if exc.code else None

        if want_in_message is None:
            if message is not None:
                failures.append(f"{check.__name__} rejected {fixture} ({what}): {message}")
        elif message is None:
            failures.append(f"{check.__name__} did not reject {fixture} ({what})")
        elif want_in_message not in message:
            failures.append(
                f"{check.__name__} rejected {fixture} without naming {want_in_message} "
                f"({what}) — it may have exited for another reason: {message}"
            )

    if failures:
        sys.exit("::error::chart_assert selftest: " + "; ".join(failures))
    print(f"ok — {len(cases)} selftest fixtures behaved as expected")


COMMANDS = {"serviceaccounts": serviceaccounts, "urlquery": urlquery, "selftest": selftest}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS:
        sys.exit(f"usage: chart_assert.py {{{'|'.join(COMMANDS)}}} <rendered.yaml>...")
    if sys.argv[1] != "selftest" and len(sys.argv) < 3:
        sys.exit(f"chart_assert: {sys.argv[1]} needs at least one rendered file")
    COMMANDS[sys.argv[1]](sys.argv[2:])
