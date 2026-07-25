package helm

import (
	"encoding/json"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
)

// minimalChart builds a chart in memory rather than reading one from disk: the
// tenant chart lives in eks-agent-platform, and what is under test here is
// portal's render path, not that chart's content.
func minimalChart(templates ...*chart.File) *chart.Chart {
	return &chart.Chart{
		Metadata:  &chart.Metadata{APIVersion: "v2", Name: "probe", Version: "0.1.0"},
		Templates: templates,
	}
}

// Values reach Render as a map[string]interface{} decoded from the operation
// row's JSONB (worker/tenant_apply.go), so every JSON number is a float64 —
// there is no int anywhere on this path. Go renders a float64 with %v, which
// switches to scientific notation above ~1e6, and Helm's `{{ }}` uses exactly
// that. A Platform datastore's queue.messageRetentionSeconds tops out at
// 1209600 (14 days, the CRD's maximum), which lands past the switch: the field
// renders as 1.2096e+06 and the API server rejects it as a non-integer.
//
// The tenant chart avoids it by emitting the datastores block through toYaml
// rather than field by field. This pins both halves of that: the hazard is real,
// and toYaml is the thing that dodges it — so a future "simplification" of the
// chart back to per-field interpolation fails here rather than in production.
func TestRender_LargeJSONNumbersSurviveToYAML(t *testing.T) {
	values := map[string]interface{}{}
	if err := json.Unmarshal([]byte(`{
		"datastores": [
			{"name": "work", "kind": "queue", "queue": {"messageRetentionSeconds": 1209600, "visibilityTimeoutSeconds": 30}}
		],
		"big": 1209600
	}`), &values); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := values["big"].(float64); !ok {
		t.Fatalf("precondition: a JSON number should decode to float64, got %T", values["big"])
	}

	ch := minimalChart(&chart.File{
		Name: "templates/probe.yaml",
		Data: []byte("interpolated: {{ .Values.big }}\n" +
			"coerced: {{ .Values.big | int64 }}\n" +
			"datastores:\n{{- toYaml .Values.datastores | nindent 2 }}\n"),
	})

	out, err := Render(ch, "probe", "eks-agent-platform", values)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The hazard, asserted so the reason for the toYaml rule stays visible.
	if !strings.Contains(out, "interpolated: 1.2096e+06") {
		t.Errorf("expected bare interpolation to produce scientific notation, got:\n%s", out)
	}
	// Both ways out of it.
	if !strings.Contains(out, "coerced: 1209600") {
		t.Errorf("expected | int64 to render an integer, got:\n%s", out)
	}
	if !strings.Contains(out, "messageRetentionSeconds: 1209600") {
		t.Errorf("expected toYaml to render an integer, got:\n%s", out)
	}
	if strings.Contains(out, "messageRetentionSeconds: 1.2096e+06") {
		t.Errorf("toYaml must not emit scientific notation, got:\n%s", out)
	}
}

// Render's output ordering has to be stable: the tenant write path commits it to
// a git repo, so map-iteration churn would produce a diff on every re-render of
// identical form values.
func TestRender_OrdersTemplatesDeterministically(t *testing.T) {
	ch := minimalChart(
		&chart.File{Name: "templates/zebra.yaml", Data: []byte("kind: Zebra\n")},
		&chart.File{Name: "templates/alpha.yaml", Data: []byte("kind: Alpha\n")},
	)
	first, err := Render(ch, "probe", "eks-agent-platform", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for range 5 {
		again, err := Render(ch, "probe", "eks-agent-platform", map[string]interface{}{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if again != first {
			t.Fatal("repeated renders of identical values must be byte-identical")
		}
	}
	if strings.Index(first, "Alpha") > strings.Index(first, "Zebra") {
		t.Errorf("templates should render in name order, got:\n%s", first)
	}
}

func TestRender_RequiresReleaseName(t *testing.T) {
	if _, err := Render(minimalChart(), "", "ns", nil); err == nil {
		t.Error("want an error for an empty release name")
	}
}
