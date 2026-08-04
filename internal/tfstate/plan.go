package tfstate

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// sensitiveValue stands in for a value the plan withholds.
//
// It is the string OpenTofu's own text renderer prints, character for character.
// That is deliberate: a redacted plan should read the way `tofu show` reads, and
// it gives the test that holds the two documents against each other something to
// match on. It is not a reserved word — an attribute whose real value is that
// string is indistinguishable from a withheld one, which is a cosmetic collision
// and not a disclosure. State redacts to null instead, because there the whole
// value is gone and there is nothing to render.
const sensitiveValue = "(sensitive value)"

// Plan is the machine-readable plan portal serves for a run.
//
// It is a projection, not the artifact. `tofu show -json` emits a document that
// is strictly more disclosing than the plan a human reads. It carries the root
// variable values — portal's own decrypted sensitive variables among them, with
// no marking of any kind. It carries .prior_state, a whole state representation.
// It carries .configuration, where a variable's default and a module call's
// literal arguments sit. And in .resource_changes it carries, in cleartext,
// every value the text render replaces with "(sensitive value)": sensitivity is
// recorded in a structure alongside the value rather than applied to it.
//
// So the endpoint cannot serve the artifact, and this is what it serves instead:
// the resource changes, narrowed to the attributes that actually differ, with
// values the plan marks sensitive withheld from callers who may not read this
// workspace's state.
//
// THE RULE BEHIND THE BAR. A projection sits at the bar of the co-located
// artifact carrying the same material. For state, that artifact is the tfstate
// download at ActionManageState, so the state browser withholds every attribute
// value below it — state has no reliable marking, and ParseResources says why.
// For a plan, it is the rendered plan text: `plan_output` is a field on the run
// detail response and the run log streams the same bytes, both at
// ActionViewWorkspace. Same rule, different co-located artifact, and so a
// different answer — the plan honours its markings, because unlike state it has
// them, and it withholds the values below the bar that its own text render
// withholds. Redacting more than that would not close anything: the values would
// still be one tab away, in a form nobody could reason about.
//
// The projection is an allowlist by construction. Unmarshalling into these
// structs drops every field they do not name, so a section some later tofu
// format adds cannot begin disclosing on its own — it arrives unnamed, and
// unnamed means absent.
//
// WHAT THIS DOES NOT CLAIM. That the values which survive are safe.
//
//   - tofu's markings do not follow a value into a computed attribute. A secret
//     read back out of one is unmarked here and cleartext in the text render
//     too, and portal is not in a position to improve on that.
//   - Dropping .prior_state withholds the prior state's OUTPUTS and the
//     attributes of resources with no pending change. The prior attributes of a
//     resource that IS changing come back as change.before, governed by the view
//     and the marking rather than by the allowlist.
//   - A sensitive variable of portal's own reaches a viewer through a resource
//     attribute whenever the tofu config does not ALSO declare that variable
//     sensitive, because then nothing marks it. The plan text discloses it at the
//     same bar; the fix is upstream, in the config.
//   - after_unknown is dropped, so "(known after apply)" is not recoverable from
//     this. It carries no values; it is left out because nothing reads it.
//
// The bar this holds is that the machine-readable plan discloses no more than
// the human-readable one, which before this projection was not true.
type Plan struct {
	FormatVersion   string           `json:"format_version"`
	ResourceChanges []ResourceChange `json:"resource_changes"`
	// SensitiveValuesRedacted reports that values the plan marks sensitive were
	// withheld. Named for the subset, because unmarked values ARE present in
	// cleartext — unlike the state surface's attributes_redacted, where every
	// value is gone. It is a property of the view, not of the plan: false means
	// the caller cleared ActionManageState, not that the plan held no secrets.
	SensitiveValuesRedacted bool `json:"sensitive_values_redacted,omitempty"`
}

// ResourceChange is one resource's entry in the plan.
type ResourceChange struct {
	Address       string `json:"address"`
	ModuleAddress string `json:"module_address,omitempty"`
	Mode          string `json:"mode"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	ProviderName  string `json:"provider_name"`
	Change        Change `json:"change"`
}

// Change carries the attributes that differ, and only those.
//
// The plan's own before/after are the resource's COMPLETE attribute maps, so a
// tag edit on a database ships every other attribute of that database with it —
// its bootstrap script, its connection string, whatever the provider wrote back.
// The text render hides them, down to the member: an attribute whose value is a
// map prints only the members that moved, behind "(N unchanged attributes
// hidden)". Narrowing reaches the same depth, so the sibling of a changed map
// member is never sent.
//
// Because both sides of a rotated secret redact to the same marker, a caller
// cannot recover which attributes changed by comparing these maps. It does not
// have to: every key present is one that changed. That is why there is no
// changed_keys field here and one on the state diff — the state diff nils every
// value anyway, so narrowing would buy it nothing, while here it is the only
// thing keeping unchanged attributes off the wire.
type Change struct {
	Actions []string       `json:"actions"`
	Before  map[string]any `json:"before"`
	After   map[string]any `json:"after"`
}

// rawPlan and friends name exactly the fields the projection reads. Everything
// else in the document — variables, prior_state, configuration, planned_values,
// output_changes, resource_drift, checks — has no field to land in and is
// dropped by encoding/json before any of this code runs.
type rawPlan struct {
	FormatVersion   string              `json:"format_version"`
	ResourceChanges []rawResourceChange `json:"resource_changes"`
}

type rawResourceChange struct {
	Address       string    `json:"address"`
	ModuleAddress string    `json:"module_address"`
	Mode          string    `json:"mode"`
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	ProviderName  string    `json:"provider_name"`
	Change        rawChange `json:"change"`
}

type rawChange struct {
	Actions []string       `json:"actions"`
	Before  map[string]any `json:"before"`
	After   map[string]any `json:"after"`
	// BeforeSensitive and AfterSensitive mirror the shape of Before and After
	// with booleans at the positions that are sensitive. Either may be a bare
	// bool standing for the whole side — false on the empty side of a create,
	// true where everything under it is sensitive.
	//
	// Typed as any so that ABSENT is distinguishable from false. Absent is not a
	// claim that nothing is sensitive; it is the absence of any claim, and it is
	// withheld accordingly.
	BeforeSensitive any `json:"before_sensitive"`
	AfterSensitive  any `json:"after_sensitive"`
}

// ProjectPlan parses a `tofu show -json` plan and returns what portal serves.
//
// The view decides only whether values the plan marks sensitive survive.
// Narrowing to changed attributes, dropping no-op entries and dropping every
// section outside resource_changes happen at both bars, because they are what
// the endpoint is contracted to return rather than a judgement about the caller.
func ProjectPlan(data []byte, view AttributeView) (*Plan, error) {
	var raw rawPlan
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	plan := &Plan{
		FormatVersion:           raw.FormatVersion,
		ResourceChanges:         []ResourceChange{},
		SensitiveValuesRedacted: view != AttributesFull,
	}

	for _, rc := range raw.ResourceChanges {
		if isNoOp(rc.Change.Actions) {
			continue
		}

		before, after := changedAttributes(rc.Change.Before, rc.Change.After)
		if view != AttributesFull {
			before = redactMarked(before, rc.Change.BeforeSensitive)
			after = redactMarked(after, rc.Change.AfterSensitive)
		}

		plan.ResourceChanges = append(plan.ResourceChanges, ResourceChange{
			Address:       rc.Address,
			ModuleAddress: rc.ModuleAddress,
			Mode:          rc.Mode,
			Type:          rc.Type,
			Name:          rc.Name,
			ProviderName:  rc.ProviderName,
			Change: Change{
				Actions: rc.Change.Actions,
				Before:  before,
				After:   after,
			},
		})
	}

	return plan, nil
}

// isNoOp reports whether a change does nothing.
//
// The plan lists every resource, including the ones it is not touching, each
// carrying its full attribute map. Those entries are the largest part of the
// document, the text render does not mention them at all, and the UI already
// discards them. An empty action list counts as no-op: a change that does not
// say what it does is not one this will describe.
func isNoOp(actions []string) bool {
	for _, a := range actions {
		if a != "no-op" {
			return false
		}
	}
	return true
}

// changedAttributes narrows both sides to what differs, comparing the real
// values — which is why this runs before redaction and not after. Two different
// secrets both redact to the same marker, so a narrowing that ran second would
// decide a rotated password had not moved.
//
// A nil side stays nil: it is how the plan says "this resource did not exist",
// and the UI reads it as create or destroy.
func changedAttributes(before, after map[string]any) (map[string]any, map[string]any) {
	narrowedBefore, narrowedAfter := narrowChanged(before, after)
	if before == nil {
		narrowedBefore = nil
	}
	if after == nil {
		narrowedAfter = nil
	}
	return narrowedBefore, narrowedAfter
}

// narrowChanged keeps the entries that differ between two maps, descending into
// members that are themselves maps.
//
// The descent is what matches the text render: a map attribute prints only the
// members that moved, so a sibling holding an unmarked credential is hidden
// there and has to be hidden here. Lists are kept whole — the text prints every
// element of a changed list, and dropping elements would renumber the rest and
// misdescribe the change.
func narrowChanged(before, after map[string]any) (map[string]any, map[string]any) {
	names := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		names[k] = struct{}{}
	}
	for k := range after {
		names[k] = struct{}{}
	}

	narrowedBefore := make(map[string]any, len(names))
	narrowedAfter := make(map[string]any, len(names))

	for name := range names {
		b, inBefore := before[name]
		a, inAfter := after[name]

		if inBefore && inAfter {
			if reflect.DeepEqual(b, a) {
				continue
			}
			bm, bIsMap := b.(map[string]any)
			am, aIsMap := a.(map[string]any)
			if bIsMap && aIsMap {
				subBefore, subAfter := narrowChanged(bm, am)
				narrowedBefore[name] = subBefore
				narrowedAfter[name] = subAfter
				continue
			}
		}

		if inBefore {
			narrowedBefore[name] = b
		}
		if inAfter {
			narrowedAfter[name] = a
		}
	}

	return narrowedBefore, narrowedAfter
}

// redactMarked replaces the values a change marks sensitive with the marker,
// keeping the attribute names.
//
// A bare true means the whole side is sensitive, and an ABSENT mirror means the
// document made no claim either way. Both withhold every value and keep every
// name — the names are what the diff is made of, and they are not the secret.
// Only an explicit false is taken as "nothing here is sensitive", because only
// an explicit false is tofu saying so.
func redactMarked(values map[string]any, mirror any) map[string]any {
	if values == nil {
		return nil
	}

	redacted, ok := redactValue(values, mirror).(map[string]any)
	if ok {
		return redacted
	}

	// redactValue returns something other than a map when it has decided the map
	// itself is withheld — a bare true, or a mirror it could not read.
	out := make(map[string]any, len(values))
	for k := range values {
		out[k] = sensitiveValue
	}
	return out
}

// redactValue walks a value alongside the boolean mirror the plan ships for it.
//
// The mirror is not always the same shape as the value it describes. It is a
// bare bool where a whole subtree is sensitive or a whole side is not, an object
// where the value is an object, a list where the value is a list. A key the
// mirror does not mention is not sensitive — a present mirror is evidence tofu
// ran its marking pass, and it names every subtree it marked.
//
// Everywhere the two disagree — a mirror absent entirely, a null where a marking
// should be, a mirror that says object over a value that is a scalar, a mirror
// list that runs out before the value list does — this withholds rather than
// guesses. Over-redacting is visible in the UI and someone complains; the other
// way round nobody finds out.
func redactValue(value, mirror any) any {
	if value == nil {
		return nil
	}

	switch m := mirror.(type) {
	case bool:
		if m {
			return sensitiveValue
		}
		return value
	case map[string]any:
		values, ok := value.(map[string]any)
		if !ok {
			return sensitiveValue
		}
		out := make(map[string]any, len(values))
		for k, v := range values {
			sub, marked := m[k]
			if !marked {
				out[k] = v
				continue
			}
			out[k] = redactValue(v, sub)
		}
		return out
	case []any:
		values, ok := value.([]any)
		if !ok {
			return sensitiveValue
		}
		out := make([]any, len(values))
		for i, v := range values {
			if i >= len(m) {
				out[i] = sensitiveValue
				continue
			}
			out[i] = redactValue(v, m[i])
		}
		return out
	default:
		// nil included: no marking is not the same as a marking of false.
		return sensitiveValue
	}
}
