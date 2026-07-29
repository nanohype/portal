# Vendored tenant CRD schemas

Byte-identical copies of the controller-gen output in
[`nanohype/eks-agent-platform`](https://github.com/nanohype/eks-agent-platform)
under `operators/config/crd/bases/`, at the commit recorded in
[`source.json`](source.json). **Never hand-edit them.**

## Why they are here

portal's tenant write path renders the operator's `charts/tenant` chart, writes
the result to the tenants GitOps repo, commits and pushes. Helm rendering only
proves the templates rendered — it says nothing about whether the output is a
valid `platform.nanohype.dev` custom resource.

Nothing downstream catches that either. The tenants repo's kubeconform gate runs
with `-ignore-missing-schemas`, so the org CRDs the repo exists to hold are
skipped, and portal pushes straight to `main` over a deploy key with no pull
request — that workflow runs *after* the write and could not block it even if the
schemas were present. Validation here is the only point on the path where a check
prevents the write instead of reporting it.

Resolving the schemas over the network at write time would make every verdict
depend on whatever upstream happened to be that minute, and the tempting failure
handler — skip when unreachable — is the exact shape of bug the check exists to
catch. So they live here, digest-pinned, embedded into the binary with `go:embed`.

## What validates them

`internal/tenantmanifest` verifies every digest **before** parsing a single
schema. A copy edited to admit a manifest the operator would reject aborts the
run rather than being trusted, and that check needs no network, so it holds on a
laptop too.

Validation itself runs through `k8s.io/apiextensions-apiserver` — the same code
the Kubernetes API server runs against a CRD — rather than a hand-rolled JSON
Schema walk. That matters for the failure a stock validator misses: controller-gen
emits no `additionalProperties: false`, so a misspelled spec key validates clean
and is then **pruned in silence** on arrival. `pruning.PruneWithOptions` reports
exactly the paths the apiserver would drop, so an unknown field is a rejection
here instead of a surprise later. The CRD's own `x-kubernetes-validations` rules
are evaluated too, which is how the datastore kind/config agreement rule and the
`allowedModels` / `allowedModelFamilies` exclusion get enforced.

## Keeping the pin honest

Two ways for these copies to become a lie, and `task crd:check` closes both:

1. **Tampering.** Someone edits a schema — widens an enum, drops a `required`
   entry — and validation happily passes against the weakened copy. The
   per-file SHA-256 in `source.json` catches it.
2. **A stale or hand-moved pin.** Someone bumps `upstream.ref` without
   re-vendoring, or re-vendors without moving the ref. The digests still agree
   with each other while describing a commit whose schemas are different.
   `check` reads each file from upstream **at the pinned ref** and requires the
   bytes to be identical.

Pin fidelity is the whole of the blocking gate, deliberately. Whether the pin is
also the newest thing upstream has is a different question with a different answer
every day, and answering it in CI would flip a required check red because someone
pushed to another repository. `task crd:freshness` asks that separately, on a
weekly schedule.

```sh
task crd:check              # blocking gate: digests, and bytes vs upstream at the pin
task crd:sync -- <sha>      # re-vendor and move the pin together
task crd:freshness          # scheduled only: has the pin fallen behind?
```

Re-vendor when the operator's API types change: run `crd:sync` with the new ref,
read the schema diff, and ship it with whatever validation change it implies.
