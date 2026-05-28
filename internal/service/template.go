package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/repository"
)

type TemplateService struct {
	queries *repository.Queries
	db      *pgxpool.Pool
}

func NewTemplateService(queries *repository.Queries, db *pgxpool.Pool) *TemplateService {
	return &TemplateService{queries: queries, db: db}
}

type CreateTemplateParams struct {
	OrgID                string
	Name                 string
	Description          string
	Persona              string
	DefaultValues        map[string]interface{}
	AllowedOverrides     []string
	MaxBudgetUSD         int32
	AllowedModelFamilies []string
	RequiredCompliance   []string
	CreatedBy            string
}

type UpdateTemplateParams struct {
	ID                   string
	OrgID                string
	Name                 string
	Description          string
	Persona              string
	DefaultValues        map[string]interface{} // nil = unchanged
	AllowedOverrides     *[]string              // nil = unchanged
	MaxBudgetUSD         *int32                 // nil = unchanged
	AllowedModelFamilies *[]string              // nil = unchanged
	RequiredCompliance   *[]string              // nil = unchanged
}

// List returns templates visible to the caller. Pass teamIDs=nil for the
// admin path (no scoping); pass a non-nil slice (possibly empty) for the
// non-admin path — empty means "user belongs to no teams" → zero rows.
func (s *TemplateService) List(ctx context.Context, orgID string, teamIDs []string) ([]repository.Template, error) {
	return s.queries.ListTemplates(ctx, repository.ListTemplatesParams{
		OrgID:   orgID,
		TeamIDs: teamIDs,
	})
}

func (s *TemplateService) Get(ctx context.Context, id, orgID string) (repository.Template, error) {
	return s.queries.GetTemplate(ctx, repository.GetTemplateParams{ID: id, OrgID: orgID})
}

func (s *TemplateService) Create(ctx context.Context, params CreateTemplateParams) (repository.Template, error) {
	defaults, err := jsonOrEmptyObject(params.DefaultValues)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal default_values: %w", err)
	}
	overrides, err := jsonOrEmptyArray(params.AllowedOverrides)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal allowed_overrides: %w", err)
	}
	models, err := jsonOrEmptyArray(params.AllowedModelFamilies)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal allowed_model_families: %w", err)
	}
	compliance, err := jsonOrEmptyArray(params.RequiredCompliance)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal required_compliance: %w", err)
	}

	return s.queries.CreateTemplate(ctx, repository.CreateTemplateParams{
		ID:                   ulid.Make().String(),
		OrgID:                params.OrgID,
		Name:                 params.Name,
		Description:          params.Description,
		Persona:              params.Persona,
		DefaultValues:        defaults,
		AllowedOverrides:     overrides,
		MaxBudgetUSD:         params.MaxBudgetUSD,
		AllowedModelFamilies: models,
		RequiredCompliance:   compliance,
		CreatedBy:            params.CreatedBy,
	})
}

func (s *TemplateService) Update(ctx context.Context, params UpdateTemplateParams) (repository.Template, error) {
	defaults, err := jsonOrNil(params.DefaultValues)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal default_values: %w", err)
	}
	overrides, err := jsonOrNilArray(params.AllowedOverrides)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal allowed_overrides: %w", err)
	}
	models, err := jsonOrNilArray(params.AllowedModelFamilies)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal allowed_model_families: %w", err)
	}
	compliance, err := jsonOrNilArray(params.RequiredCompliance)
	if err != nil {
		return repository.Template{}, fmt.Errorf("marshal required_compliance: %w", err)
	}

	return s.queries.UpdateTemplate(ctx, repository.UpdateTemplateParams{
		ID:                   params.ID,
		OrgID:                params.OrgID,
		Name:                 params.Name,
		Description:          params.Description,
		Persona:              params.Persona,
		DefaultValues:        defaults,
		AllowedOverrides:     overrides,
		MaxBudgetUSD:         params.MaxBudgetUSD,
		AllowedModelFamilies: models,
		RequiredCompliance:   compliance,
	})
}

func (s *TemplateService) Delete(ctx context.Context, id, orgID string) error {
	return s.queries.DeleteTemplate(ctx, repository.DeleteTemplateParams{ID: id, OrgID: orgID})
}

// ApplyToValues is the load-bearing validation method. Given a template and
// the operator's override values, it produces the final helm values the
// worker will render OR returns an error describing what the operator did
// wrong. Rules:
//
//  1. Start from `template.default_values` (the admin's curated baseline).
//  2. Apply each override only if its dotted path is in
//     `template.allowed_overrides`. Disallowed paths → error.
//  3. Cap `budget.monthlyUsd` at `template.max_budget_usd` (when > 0).
//  4. Intersect `identity.allowedModelFamilies` with the template's list.
//     Operator can narrow but not broaden.
//  5. Ensure `platform.compliance.<flag>` is `true` for every required flag.
//
// The function is pure (no DB writes) and gets called from the tenant
// create handler before the tenant_operation row is persisted, so a
// rejected override produces a clean 400 with no orphaned state.
func (s *TemplateService) ApplyToValues(template repository.Template, overrides map[string]interface{}) (map[string]interface{}, error) {
	defaults := map[string]interface{}{}
	if len(template.DefaultValues) > 0 {
		if err := json.Unmarshal(template.DefaultValues, &defaults); err != nil {
			return nil, fmt.Errorf("template defaults are not a JSON object: %w", err)
		}
	}

	allowedPaths := map[string]bool{}
	if len(template.AllowedOverrides) > 0 {
		var paths []string
		if err := json.Unmarshal(template.AllowedOverrides, &paths); err != nil {
			return nil, fmt.Errorf("template allowed_overrides is not a JSON array: %w", err)
		}
		for _, p := range paths {
			allowedPaths[p] = true
		}
	}

	// Apply each override path one at a time. Each path is a dotted route
	// like "platform.displayName" → defaults["platform"]["displayName"].
	merged := deepCopy(defaults)
	for _, path := range sortedKeys(flatten(overrides)) {
		if !allowedPaths[path] {
			return nil, fmt.Errorf("override not allowed by template: %s", path)
		}
		setPath(merged, path, getPath(overrides, path))
	}

	if template.MaxBudgetUSD > 0 {
		// budget.monthlyUsd comes through after a JSON round-trip
		// (`deepCopy`), so the runtime type is float64. The old code also
		// had an `int` branch as a half-defense in case the caller built
		// the map in Go directly; in practice that path was dead.
		// asFloat handles every numeric shape a caller could plausibly
		// pass without us silently bypassing the cap.
		if got, ok := asFloat(getPath(merged, "budget.monthlyUsd")); ok {
			if got > float64(template.MaxBudgetUSD) {
				return nil, fmt.Errorf("budget.monthlyUsd %v exceeds template cap %d", got, template.MaxBudgetUSD)
			}
		}
	}

	if len(template.AllowedModelFamilies) > 0 {
		var allowed []string
		if err := json.Unmarshal(template.AllowedModelFamilies, &allowed); err != nil {
			return nil, fmt.Errorf("template allowed_model_families is not a JSON array: %w", err)
		}
		if len(allowed) > 0 {
			current := stringSliceFromPath(merged, "identity.allowedModelFamilies")
			if len(current) > 0 {
				allowedSet := map[string]bool{}
				for _, f := range allowed {
					allowedSet[f] = true
				}
				for _, f := range current {
					if !allowedSet[f] {
						return nil, fmt.Errorf("model family %q is not allowed by template", f)
					}
				}
			} else {
				// Default to the template's allowed set if user didn't override.
				setPath(merged, "identity.allowedModelFamilies", anySlice(allowed))
			}
		}
	}

	if len(template.RequiredCompliance) > 0 {
		var required []string
		if err := json.Unmarshal(template.RequiredCompliance, &required); err != nil {
			return nil, fmt.Errorf("template required_compliance is not a JSON array: %w", err)
		}
		for _, flag := range required {
			path := "platform.compliance." + flag
			v := getPath(merged, path)
			if v != true {
				return nil, fmt.Errorf("template requires platform.compliance.%s = true", flag)
			}
		}
	}

	return merged, nil
}

// flatten walks a nested map[string]interface{} and produces a flat
// path→value map keyed by dotted paths. Used to enumerate the paths an
// operator's override request touches so we can compare against the
// template's allowed_overrides allowlist.
func flatten(in map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	var walk func(prefix string, v interface{})
	walk = func(prefix string, v interface{}) {
		if m, ok := v.(map[string]interface{}); ok && prefix != "" {
			if len(m) == 0 {
				out[prefix] = m
				return
			}
			for k, vv := range m {
				walk(prefix+"."+k, vv)
			}
			return
		}
		if m, ok := v.(map[string]interface{}); ok {
			for k, vv := range m {
				walk(k, vv)
			}
			return
		}
		out[prefix] = v
	}
	walk("", in)
	return out
}

// getPath walks dotted keys into a nested map. Returns nil when any segment
// isn't a map or when a key is missing.
func getPath(m map[string]interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	var cur interface{} = m
	for _, p := range parts {
		mp, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = mp[p]
	}
	return cur
}

// setPath writes a value into a nested map, creating intermediate maps as
// needed. Pure mutate on the receiver; callers pass deep copies to avoid
// surprises.
func setPath(m map[string]interface{}, path string, value interface{}) {
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[p] = next
		}
		cur = next
		_ = i
	}
	cur[parts[len(parts)-1]] = value
}

// deepCopy round-trips through JSON. Slow but correct; ApplyToValues is
// called once per tenant create, not in a hot path.
func deepCopy(m map[string]interface{}) map[string]interface{} {
	b, _ := json.Marshal(m)
	var out map[string]interface{}
	_ = json.Unmarshal(b, &out)
	if out == nil {
		return map[string]interface{}{}
	}
	return out
}

// asFloat accepts any of the numeric runtime types a JSON-decoded map can
// carry (float64 by default; int/int32/int64 if a caller built the map in
// Go) plus json.Number for decoders configured with UseNumber. Returns
// (value, true) on success or (0, false) for non-numeric input. Used by the
// budget cap so a caller can't silently bypass it by handing us an int.
func asFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func stringSliceFromPath(m map[string]interface{}, path string) []string {
	v := getPath(m, path)
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func anySlice(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func jsonOrEmptyObject(m map[string]interface{}) (json.RawMessage, error) {
	if m == nil {
		return json.RawMessage("{}"), nil
	}
	return json.Marshal(m)
}

func jsonOrEmptyArray(xs []string) (json.RawMessage, error) {
	if xs == nil {
		return json.RawMessage("[]"), nil
	}
	return json.Marshal(xs)
}

// jsonOrNil — Update path: nil → don't change column, empty/populated → replace.
func jsonOrNil(m map[string]interface{}) (json.RawMessage, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func jsonOrNilArray(xs *[]string) (json.RawMessage, error) {
	if xs == nil {
		return nil, nil
	}
	return json.Marshal(*xs)
}
