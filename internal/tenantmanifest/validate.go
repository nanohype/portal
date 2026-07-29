// Package tenantmanifest validates a rendered tenant manifest against the
// eks-agent-platform CRD schemas before portal writes it to the GitOps repo.
//
// The write path renders the operator's `charts/tenant` chart, writes the result,
// commits and pushes. Helm rendering only proves the templates rendered — it says
// nothing about whether the output is a valid `platform.nanohype.dev` custom
// resource. Downstream, the tenants repo's kubeconform gate runs with
// `-ignore-missing-schemas`, so the org CRDs the repo exists to hold are skipped,
// and portal pushes straight to `main` over a deploy key with no pull request, so
// that workflow runs *after* the write and could not block it even if the schemas
// were present.
//
// So this is the only point on the path where a check prevents the write rather
// than reporting it, and the first thing that would otherwise notice a malformed
// manifest is the operator's reconcile loop — or ArgoCD failing the sync for a
// whole cluster directory, because the ApplicationSet's unit is the directory,
// not the file.
//
// ── Why the apiserver's own machinery ──
//
// Validation runs through `k8s.io/apiextensions-apiserver`, the same code the
// Kubernetes API server runs against a CRD, rather than a hand-rolled JSON Schema
// walk. That matters most for the failure mode a stock validator misses:
// controller-gen emits no `additionalProperties: false`, so a misspelled spec key
// validates clean and is then *pruned in silence* on arrival. A manifest can be
// accepted, applied, and quietly missing the field someone set.
//
// `pruning.PruneWithOptions` answers exactly that question — it reports the paths
// the apiserver would drop — so an unknown field is a rejection here instead of a
// surprise later.
//
// ── Why the schemas are vendored ──
//
// Resolving them over the network at write time would make every verdict depend
// on whatever upstream happened to be that minute, and the tempting failure
// handler (skip when unreachable) is the exact shape of bug the check exists to
// catch. They live in `schemas/`, digest-pinned in `source.json`, embedded into
// the binary. Digests are verified before any schema is parsed, so a copy edited
// to admit a manifest the operator would reject aborts the run instead of being
// trusted.
package tenantmanifest

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	celschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"sigs.k8s.io/yaml"
)

//go:embed schemas
var schemaFS embed.FS

const schemaDir = "schemas"

// Budgets for CEL evaluation. These mirror the apiserver's own defaults closely
// enough for the handful of short rules these CRDs declare; a rule that blew
// through them would be rejected here and by the apiserver alike.
const (
	celPerCallLimit = 1_000_000
	celCostBudget   = 10_000_000
)

// Expect is what portal believes it is writing. Checking the rendered manifest
// against it catches the class of render bug that produces a structurally valid
// CR for the wrong tenant — which would be applied successfully, to the wrong
// namespace, and look like a success in the operation row.
type Expect struct {
	// Tenant is the owning team, matching `Tenant.metadata.name` and
	// `Platform.spec.tenant`.
	Tenant string
	// Platform is the workload name, matching `Platform.metadata.name`.
	Platform string
}

type sourceManifest struct {
	Upstream struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
		Ref        string `json:"ref"`
	} `json:"upstream"`
	Files []struct {
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

type crdSchema struct {
	group      string
	kind       string
	namespaced bool
	props      *apiextensions.JSONSchemaProps
	structural *structuralschema.Structural
	validator  validation.SchemaValidator
	// cel evaluates the CRD's own x-kubernetes-validations. The Platform schema
	// carries five, including the rule that a datastore's config block must match
	// its kind and that allowedModels and allowedModelFamilies are mutually
	// exclusive. Without this portal would be a weaker gate than the four tenant
	// app repos, which assert those rules in their own CI.
	cel *celschema.Validator
}

// Validator holds the compiled CRD schemas. Build one at startup and reuse it;
// compiling is the expensive part and the schemas never change at runtime.
type Validator struct {
	// byKind is keyed on lowercase kind — the three kinds are unique across the
	// two groups this validates, and the group is asserted separately.
	byKind map[string]*crdSchema
	ref    string
}

// New reads the embedded schemas, verifies their digests, and compiles each into
// an apiserver validator. Any failure is fatal: a validator that could not
// compile its schemas must not be used to approve a write.
func New() (*Validator, error) {
	raw, err := schemaFS.ReadFile(path.Join(schemaDir, "source.json"))
	if err != nil {
		return nil, fmt.Errorf("read schemas/source.json: %w", err)
	}
	var src sourceManifest
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("parse schemas/source.json: %w", err)
	}
	if len(src.Files) == 0 {
		return nil, fmt.Errorf("schemas/source.json declares no files — it would validate nothing")
	}
	if len(src.Upstream.Ref) != 40 {
		return nil, fmt.Errorf(
			"schemas/source.json upstream.ref is %q, not a full commit sha — a branch would make the pin meaningless",
			src.Upstream.Ref,
		)
	}

	// Every .yaml present must be declared. An undeclared schema is un-digested
	// and un-compared, which is the blind spot the manifest exists to remove.
	declared := map[string]string{}
	for _, f := range src.Files {
		declared[f.File] = f.SHA256
	}
	entries, err := schemaFS.ReadDir(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("read schemas/: %w", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if _, ok := declared[e.Name()]; !ok {
			return nil, fmt.Errorf("schemas/%s is not declared in source.json — add it with a digest, or delete it", e.Name())
		}
	}

	v := &Validator{byKind: map[string]*crdSchema{}, ref: src.Upstream.Ref}
	for _, f := range src.Files {
		body, err := schemaFS.ReadFile(path.Join(schemaDir, f.File))
		if err != nil {
			return nil, fmt.Errorf("read schemas/%s: %w", f.File, err)
		}
		// Verified before parsing, not after: a tampered schema must never reach
		// the compiler, because from there on the gate's only notion of the CRD
		// is that file.
		sum := hex.EncodeToString(sha256Sum(body))
		if sum != declared[f.File] {
			return nil, fmt.Errorf(
				"schemas/%s was edited in place: source.json records %s, on disk %s",
				f.File, declared[f.File], sum,
			)
		}
		compiled, err := compile(body)
		if err != nil {
			return nil, fmt.Errorf("compile schemas/%s: %w", f.File, err)
		}
		v.byKind[strings.ToLower(compiled.kind)] = compiled
	}
	return v, nil
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// compile turns one CRD document into a validator plus the structural schema the
// pruning check needs.
func compile(body []byte) (*crdSchema, error) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(body, &crd); err != nil {
		return nil, fmt.Errorf("not a parseable CustomResourceDefinition: %w", err)
	}
	if crd.Kind != "CustomResourceDefinition" {
		return nil, fmt.Errorf("kind is %q, not CustomResourceDefinition — refusing it", crd.Kind)
	}
	if len(crd.Spec.Versions) == 0 {
		return nil, fmt.Errorf("declares no versions")
	}

	// The served+storage version is the one a manifest is applied against.
	var chosen *apiextensionsv1.CustomResourceDefinitionVersion
	for i := range crd.Spec.Versions {
		ver := &crd.Spec.Versions[i]
		if ver.Storage {
			chosen = ver
			break
		}
	}
	if chosen == nil {
		chosen = &crd.Spec.Versions[0]
	}
	if chosen.Schema == nil || chosen.Schema.OpenAPIV3Schema == nil {
		return nil, fmt.Errorf("version %s carries no openAPIV3Schema", chosen.Name)
	}

	internal := &apiextensions.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
		chosen.Schema.OpenAPIV3Schema, internal, nil,
	); err != nil {
		return nil, fmt.Errorf("convert schema for version %s: %w", chosen.Name, err)
	}

	structural, err := structuralschema.NewStructural(internal)
	if err != nil {
		return nil, fmt.Errorf("build structural schema: %w", err)
	}
	validator, _, err := validation.NewSchemaValidator(internal)
	if err != nil {
		return nil, fmt.Errorf("build schema validator: %w", err)
	}

	return &crdSchema{
		group:      crd.Spec.Group,
		kind:       crd.Spec.Names.Kind,
		namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
		props:      internal,
		structural: structural,
		validator:  validator,
		cel:        celschema.NewValidator(structural, true, celPerCallLimit),
	}, nil
}

// Ref is the upstream commit the vendored schemas came from, for logging so a
// rejection can be traced to the schema version that produced it.
func (v *Validator) Ref() string { return v.ref }

// Validate checks every document in a rendered manifest. It returns a single
// error listing everything wrong, rather than the first problem, because an
// operator fixing a manifest wants the whole list.
func (v *Validator) Validate(manifest []byte, want Expect) error {
	docs, err := splitYAML(manifest)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("rendered manifest contains no documents")
	}

	var problems []string
	seenKinds := map[string]string{} // kind → metadata.name

	for i, doc := range docs {
		where := fmt.Sprintf("document %d", i+1)
		obj, ok := doc.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: not a mapping", where))
			continue
		}

		apiVersion, _ := obj["apiVersion"].(string)
		kind, _ := obj["kind"].(string)
		if apiVersion == "" || kind == "" {
			problems = append(problems, fmt.Sprintf("%s: missing apiVersion or kind", where))
			continue
		}
		where = fmt.Sprintf("%s (%s)", kind, apiVersion)

		schema, known := v.byKind[strings.ToLower(kind)]
		if !known {
			problems = append(problems, fmt.Sprintf(
				"%s: no vendored CRD schema for this kind — portal validates only the tenant CRs it renders", where))
			continue
		}
		if group := strings.SplitN(apiVersion, "/", 2)[0]; group != schema.group {
			problems = append(problems, fmt.Sprintf("%s: apiVersion group is %q, but the CRD declares %q", where, group, schema.group))
			continue
		}

		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if name == "" {
			problems = append(problems, fmt.Sprintf("%s: metadata.name is empty", where))
			continue
		}
		if prev, dup := seenKinds[kind]; dup {
			problems = append(problems, fmt.Sprintf("%s: a second %s (%s) — the first was %s", where, kind, name, prev))
		}
		seenKinds[kind] = name

		// Scope, from the CRD's own declaration rather than a local assumption.
		// A namespaced CR with no namespace lands wherever the applier's context
		// points; a cluster-scoped one carrying a namespace is rejected outright.
		ns, _ := meta["namespace"].(string)
		if schema.namespaced && ns == "" {
			problems = append(problems, fmt.Sprintf("%s %q: is namespaced but sets no metadata.namespace", where, name))
		}
		if !schema.namespaced && ns != "" {
			problems = append(problems, fmt.Sprintf("%s %q: is cluster-scoped but sets metadata.namespace=%q", where, name, ns))
		}

		// The schema check the apiserver would run.
		if result := schema.validator.Validate(obj); len(result.Errors) > 0 {
			for _, e := range result.Errors {
				problems = append(problems, fmt.Sprintf("%s %q: %s", where, name, e.Error()))
			}
		}

		// The CRD's own CEL rules, as the apiserver evaluates them on a create.
		// `oldObj` is nil deliberately: transition rules (`self == oldSelf`, the
		// immutability guards) have nothing to compare against on a first write and
		// the evaluator skips them, which is the same thing that happens when the
		// resource is created for real.
		if schema.cel != nil {
			errs, _ := schema.cel.Validate(
				context.Background(), nil, schema.structural, obj, nil, celCostBudget,
			)
			for _, e := range errs {
				problems = append(problems, fmt.Sprintf("%s %q: %s", where, name, e.Error()))
			}
		}

		// The fields the apiserver would prune in silence. controller-gen emits no
		// `additionalProperties: false`, so this is the only thing standing between
		// a misspelled spec key and a CR that applies cleanly without it.
		pruned := pruning.PruneWithOptions(
			deepCopyJSON(obj), schema.structural, true,
			structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true},
		)
		for _, p := range pruned {
			problems = append(problems, fmt.Sprintf(
				"%s %q: unknown field %s — the apiserver would drop it silently", where, name, p))
		}
	}

	problems = append(problems, crossRefs(docs, seenKinds, want)...)

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf(
			"rendered manifest is not a valid tenant declaration (schemas from %s):\n  - %s",
			v.ref[:12], strings.Join(problems, "\n  - "),
		)
	}
	return nil
}

// crossRefs checks the relationships no single-document schema can express: that
// the CRs agree with each other, and that together they describe the tenant
// portal thinks it is writing.
func crossRefs(docs []any, seen map[string]string, want Expect) []string {
	var problems []string

	if want.Tenant != "" {
		if got, ok := seen["Tenant"]; ok && got != want.Tenant {
			problems = append(problems, fmt.Sprintf(
				"Tenant is named %q but this operation is for %q", got, want.Tenant))
		}
	}
	if want.Platform != "" {
		if got, ok := seen["Platform"]; ok && got != want.Platform {
			problems = append(problems, fmt.Sprintf(
				"Platform is named %q but this operation is for %q", got, want.Platform))
		}
	}

	for _, doc := range docs {
		obj, ok := doc.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := obj["kind"].(string); kind != "Platform" {
			continue
		}
		spec, _ := obj["spec"].(map[string]any)
		tenant, _ := spec["tenant"].(string)
		if tenant == "" {
			problems = append(problems, "Platform.spec.tenant is empty")
			continue
		}
		// The Tenant CR is what grants the Platform a home. A Platform pointing at
		// a tenant this manifest does not declare reconciles against whatever
		// Tenant happens to exist on the cluster under that name.
		if declared, ok := seen["Tenant"]; ok && declared != tenant {
			problems = append(problems, fmt.Sprintf(
				"Platform.spec.tenant is %q but the Tenant declared here is %q", tenant, declared))
		}
		if want.Tenant != "" && tenant != want.Tenant {
			problems = append(problems, fmt.Sprintf(
				"Platform.spec.tenant is %q but this operation is for %q", tenant, want.Tenant))
		}
	}
	return problems
}

// splitYAML parses a multi-document manifest. Empty documents are dropped — Helm
// templates routinely render a trailing `---`.
func splitYAML(manifest []byte) ([]any, error) {
	var docs []any
	for i, chunk := range strings.Split(string(manifest), "\n---") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(chunk, "---"))
		if trimmed == "" {
			continue
		}
		var doc any
		if err := yaml.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, fmt.Errorf("document %d is not parseable YAML: %w", i+1, err)
		}
		if doc == nil {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// deepCopyJSON copies the decoded object so pruning can report against a throwaway
// rather than mutating the object the schema validator already saw.
func deepCopyJSON(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyJSON(t)
	case []any:
		cp := make([]any, len(t))
		for i, e := range t {
			cp[i] = deepCopyValue(e)
		}
		return cp
	default:
		return v
	}
}
