# The vendored Cluster XRD

`cluster-xrd.yaml` is a byte-identical copy of `apis/cluster/definition.yaml` in
[nanohype/eks-fleet](https://github.com/nanohype/eks-fleet) at the commit
recorded in `source.json`. It is the schema portal's rendered `Cluster` CR is
checked against by `xrd_contract_test.go`.

## Why a copy

portal renders a CR it does not own the schema for. Resolving that schema over
the network at test time would make every verdict depend on whatever upstream
happens to be that minute, and the tempting failure handler — skip the check
when the fetch fails — is exactly the shape of bug the check exists to catch.
So the schema lives here, and the verdict is a pure function of the commit
under test.

That leaves two ways for the copy to become a lie, and `task xrd:check` closes
both:

1. **Tampering.** Someone widens an enum or drops a field in the vendored copy
   and the test happily validates against the weakened schema, because the
   test's only notion of the schema *is* that copy. `source.json` records a
   SHA-256, the test verifies it before parsing anything, and `xrd:check`
   re-verifies it.
2. **A stale or hand-moved pin.** Someone bumps `upstream.ref` without
   re-vendoring, or re-vendors without moving the pin — the digest still agrees
   with itself while describing a commit whose schema is different. `xrd:check`
   reads the file from upstream *at the pinned ref* and requires the bytes to be
   identical, so the pin and its contents cannot diverge.

Pin fidelity is the whole of the blocking gate, deliberately. Whether the pin is
also the newest thing upstream has is a different question with a different
answer every day, and answering it here would make a required check flip red
because someone pushed to another repository. `task xrd:freshness` asks that
separately, on a schedule, never in pull-request CI.

## Re-vendoring

When the XRD changes upstream:

```sh
task xrd:sync -- <upstream-sha>   # rewrites the copy and the pin together
go test ./internal/clusterspec/   # says what the change means for the render
```

The test names the gap in both directions: a field portal renders that the
schema no longer has (it would be pruned at admission, silently), and a field
the schema has that portal neither expresses nor lists in
`unexpressedXRDFields`. The second is not automatically a bug — plenty of XRD
fields are org-wide constants portal should leave alone — but it must be a
decision someone wrote down rather than one nobody made.

Upstream resolves two ways, both deterministic: `$EKS_FLEET_DIR` when a local
checkout is set (its HEAD must equal the pinned ref), otherwise
raw.githubusercontent.com at the pinned ref.
