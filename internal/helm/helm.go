// Package helm renders Helm charts in-process so the worker doesn't need a
// `helm` binary in its container. Used by the tenant-write path to turn a
// form's worth of values into the YAML that ArgoCD will eventually apply.
//
// The chart source is a directory on disk — typically a clone of the EAP
// charts repo maintained by internal/git's CloneOrPull. The chart cache
// here keeps loaded *chart.Chart objects in memory keyed by chart name so
// repeated tenant writes don't re-parse the chart from disk.
package helm

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// Cache loads + memoizes charts from a base directory (typically the local
// clone of the EAP charts repo). Charts are loaded once per process and
// re-loaded on Reset (called after a `git pull` brings new template files
// in).
type Cache struct {
	base   string
	mu     sync.RWMutex
	charts map[string]*chart.Chart
}

func NewCache(base string) *Cache {
	return &Cache{base: base, charts: map[string]*chart.Chart{}}
}

// Reset drops every cached chart. Call this after `git pull` updates the
// underlying repo so the next Load picks up the new templates.
func (c *Cache) Reset() {
	c.mu.Lock()
	c.charts = map[string]*chart.Chart{}
	c.mu.Unlock()
}

// Load returns the named chart, parsing it from disk on first access.
// `name` is the directory under base (e.g. "tenant" → <base>/charts/tenant).
func (c *Cache) Load(name string) (*chart.Chart, error) {
	c.mu.RLock()
	if ch, ok := c.charts[name]; ok {
		c.mu.RUnlock()
		return ch, nil
	}
	c.mu.RUnlock()

	path := filepath.Join(c.base, "charts", name)
	ch, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load chart %s: %w", name, err)
	}
	c.mu.Lock()
	c.charts[name] = ch
	c.mu.Unlock()
	return ch, nil
}

// Render runs the chart's templates against `values`, merging with chart
// defaults, and returns a single combined YAML stream — one `---` per
// rendered template. The helm metadata-only files (NOTES.txt, the chart's
// own Chart.yaml/values.yaml) are skipped.
//
// `releaseName` and `namespace` shape `.Release.Name` / `.Release.Namespace`
// inside templates. Pick names that match what the operator will see in
// ArgoCD; the helm "release" concept is just a value-injection harness here,
// not an actual `helm install` operation.
func Render(ch *chart.Chart, releaseName, namespace string, values map[string]interface{}) (string, error) {
	if releaseName == "" {
		return "", fmt.Errorf("releaseName is required")
	}
	if namespace == "" {
		namespace = "default"
	}

	// Merge user values over chart defaults, the same way `helm template`
	// would. chartutil.ToRenderValues handles the .Release / .Chart /
	// .Capabilities scaffolding helm templates expect.
	merged, err := chartutil.CoalesceValues(ch, values)
	if err != nil {
		return "", fmt.Errorf("merge values: %w", err)
	}
	render, err := chartutil.ToRenderValues(ch, merged, chartutil.ReleaseOptions{
		Name:      releaseName,
		Namespace: namespace,
		IsInstall: true,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("render values: %w", err)
	}

	out, err := engine.Engine{}.Render(ch, render)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}

	// Deterministic ordering for stable diffs across runs of the same form
	// values. Without this, helm's map iteration produces churn-only diffs
	// in the tenants repo every commit.
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sortStrings(keys)

	var b strings.Builder
	for _, k := range keys {
		// engine returns NOTES.txt + everything; skip the non-manifest files.
		if strings.HasSuffix(k, "NOTES.txt") {
			continue
		}
		v := strings.TrimSpace(out[k])
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n---\n")
		}
		b.WriteString("# Source: ")
		b.WriteString(k)
		b.WriteString("\n")
		b.WriteString(v)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// sortStrings is a tiny stand-in for sort.Strings so we don't import sort
// just to maintain deterministic key ordering.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
