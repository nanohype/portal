package tenantmanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate's self-test.
//
// This validator is the only thing on portal's write path that can prevent a
// malformed tenant manifest from reaching a cluster, so a positive-path test is
// not enough — a validator that accepted everything would pass one. Each case
// below breaks the manifest a specific way and asserts the rejection, and the
// ways were chosen because each one fails quietly in production if it gets
// through: a pruned field applies as a success with the field missing, a wrong
// tenant name applies to the wrong namespace, a scope mistake lands the CR
// somewhere nobody looks.
//
// The fixture is a real production tenant declaration rather than a minimal
// hand-written one, so the schema is exercised against the shape the chart
// actually renders.

func fixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "valid.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func newValidator(t *testing.T) *Validator {
	t.Helper()
	v, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

// The positive control. Without it, every rejection below could be explained by
// a validator that rejects everything.
func TestValidAcceptsARealTenantDeclaration(t *testing.T) {
	v := newValidator(t)
	if err := v.Validate(fixture(t), Expect{Tenant: "strategy", Platform: "competitive-intelligence"}); err != nil {
		t.Fatalf("a real tenant manifest was rejected: %v", err)
	}
}

func TestSchemasCompileAndDeclareTheExpectedKinds(t *testing.T) {
	v := newValidator(t)
	for _, kind := range []string{"tenant", "platform", "budgetpolicy"} {
		if _, ok := v.byKind[kind]; !ok {
			t.Errorf("no compiled schema for kind %q", kind)
		}
	}
	if len(v.Ref()) != 40 {
		t.Errorf("Ref() is %q, expected a full commit sha", v.Ref())
	}
}

func TestRejections(t *testing.T) {
	v := newValidator(t)
	base := string(fixture(t))
	want := Expect{Tenant: "strategy", Platform: "competitive-intelligence"}

	cases := []struct {
		name    string
		mutate  func(string) string
		expect  Expect
		wantMsg string
	}{
		{
			// The failure this validator exists for. controller-gen emits no
			// `additionalProperties: false`, so a misspelled key passes a stock
			// JSON Schema check and is then dropped on arrival — the CR applies
			// successfully without the field anyone set.
			name:    "a misspelled spec field the apiserver would prune silently",
			mutate:  func(s string) string { return strings.Replace(s, "  tenant: strategy", "  tenantt: strategy", 1) },
			expect:  want,
			wantMsg: "unknown field",
		},
		{
			name: "an unknown nested field under identity",
			mutate: func(s string) string {
				return strings.Replace(s, "  identity:", "  identity:\n    notARealField: true", 1)
			},
			expect:  want,
			wantMsg: "unknown field",
		},
		{
			// A render bug that produces a structurally valid CR for the wrong
			// tenant would otherwise apply cleanly, to the wrong namespace, and
			// look like a success in the operation row.
			name:    "a Platform for a different tenant than the operation",
			mutate:  func(s string) string { return s },
			expect:  Expect{Tenant: "growth", Platform: "competitive-intelligence"},
			wantMsg: "this operation is for",
		},
		{
			name:    "a Platform named something other than the operation's platform",
			mutate:  func(s string) string { return s },
			expect:  Expect{Tenant: "strategy", Platform: "digest-pipeline"},
			wantMsg: "this operation is for",
		},
		{
			// Platform.spec.tenant pointing at a tenant this manifest does not
			// declare reconciles against whatever Tenant happens to exist on the
			// cluster under that name.
			name:    "Platform.spec.tenant disagreeing with the declared Tenant",
			mutate:  func(s string) string { return strings.Replace(s, "  tenant: strategy", "  tenant: growth", 1) },
			expect:  Expect{},
			wantMsg: "but the Tenant declared here is",
		},
		{
			name:    "a namespaced CR with no namespace",
			mutate:  func(s string) string { return strings.Replace(s, "  namespace: tenants-strategy\n", "", 1) },
			expect:  want,
			wantMsg: "sets no metadata.namespace",
		},
		{
			// The Tenant CR is cluster-scoped; a namespace on it is meaningless
			// and signals a template that lost track of which CR it is rendering.
			name: "a namespace on the cluster-scoped Tenant",
			mutate: func(s string) string {
				return strings.Replace(s, "kind: Tenant\nmetadata:\n  name: strategy",
					"kind: Tenant\nmetadata:\n  name: strategy\n  namespace: tenants-strategy", 1)
			},
			expect:  want,
			wantMsg: "cluster-scoped but sets metadata.namespace",
		},
		{
			name:    "a document with no kind",
			mutate:  func(s string) string { return s + "\n---\napiVersion: v1\nmetadata:\n  name: stray\n" },
			expect:  want,
			wantMsg: "missing apiVersion or kind",
		},
		{
			// Only the three tenant CRs belong in this file. Anything else is a
			// chart rendering something portal does not own.
			name: "a kind portal does not render",
			mutate: func(s string) string {
				return s + "\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stray\n"
			},
			expect:  want,
			wantMsg: "no vendored CRD schema for this kind",
		},
		{
			name: "the right kind under the wrong API group",
			mutate: func(s string) string {
				return strings.Replace(s, "apiVersion: platform.nanohype.dev/v1alpha1\nkind: Platform",
					"apiVersion: agents.nanohype.dev/v1alpha1\nkind: Platform", 1)
			},
			expect:  want,
			wantMsg: "but the CRD declares",
		},
		{
			name:    "an empty metadata.name",
			mutate:  func(s string) string { return strings.Replace(s, "  name: strategy", `  name: ""`, 1) },
			expect:  Expect{},
			wantMsg: "metadata.name is empty",
		},
		{
			name:    "unparseable YAML",
			mutate:  func(s string) string { return s + "\n---\n\tthis: [is not: yaml\n" },
			expect:  want,
			wantMsg: "not parseable YAML",
		},
		{
			name:    "an empty manifest",
			mutate:  func(string) string { return "" },
			expect:  want,
			wantMsg: "no documents",
		},
		{
			name:    "two Platforms in one file",
			mutate:  func(s string) string { return s + "\n---\n" + platformOnly(s) },
			expect:  want,
			wantMsg: "a second Platform",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate([]byte(tc.mutate(base)), tc.expect)
			if err == nil {
				t.Fatalf("accepted a manifest that should have been rejected")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("rejected for the wrong reason:\n  want substring: %s\n  got: %v", tc.wantMsg, err)
			}
		})
	}
}

// The CRD carries five x-kubernetes-validations rules. These assert portal
// evaluates them, so it is not a weaker gate than the tenant app repos that
// check the same rules in their own CI.
func TestCELRulesFromTheCRDAreEnforced(t *testing.T) {
	v := newValidator(t)
	base := string(fixture(t))

	// allowedModels and allowedModelFamilies are declared mutually exclusive.
	broken := strings.Replace(base, "    allowedModels:", "    allowedModelFamilies:\n      - anthropic\n    allowedModels:", 1)
	if broken == base {
		t.Skip("fixture does not set allowedModels; nothing to make mutually exclusive")
	}
	err := v.Validate([]byte(broken), Expect{})
	if err == nil {
		t.Fatal("accepted a Platform setting both allowedModels and allowedModelFamilies")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("rejected, but not by the CRD's CEL rule: %v", err)
	}
}

// platformOnly extracts the Platform document from the fixture so a duplicate can
// be appended without hand-maintaining a second copy.
func platformOnly(manifest string) string {
	for _, doc := range strings.Split(manifest, "\n---") {
		if strings.Contains(doc, "kind: Platform") {
			return strings.TrimSpace(strings.TrimPrefix(doc, "---"))
		}
	}
	return ""
}
