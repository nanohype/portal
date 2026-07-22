package tfstate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// StateDiff represents the difference between two state versions.
type StateDiff struct {
	Added     int            `json:"added"`
	Removed   int            `json:"removed"`
	Changed   int            `json:"changed"`
	Unchanged int            `json:"unchanged"`
	Diffs     []ResourceDiff `json:"diffs"`
}

// ResourceDiff represents the change to a single resource between two states.
//
// Before and After are the same state attributes ParseResources returns and
// carry the same disclosure, so they follow the same AttributeView: under
// AttributesRedacted their values are null and AttributesRedacted is true.
// ChangedKeys is computed from the full attributes either way — which
// attribute changed is inventory, what it changed to is not.
type ResourceDiff struct {
	Type               string                 `json:"type"`
	Name               string                 `json:"name"`
	Module             string                 `json:"module"`
	Action             string                 `json:"action"` // "added", "removed", "changed", "unchanged"
	Before             map[string]interface{} `json:"before,omitempty"`
	After              map[string]interface{} `json:"after,omitempty"`
	ChangedKeys        []string               `json:"changed_keys,omitempty"`
	AttributesRedacted bool                   `json:"attributes_redacted,omitempty"`
}

// resourceKey builds a unique identifier for a resource across states.
func resourceKey(r Resource) string {
	if r.Module != "" {
		return fmt.Sprintf("%s.%s.%s", r.Module, r.Type, r.Name)
	}
	return fmt.Sprintf("%s.%s", r.Type, r.Name)
}

// DiffStates compares two state file blobs and returns a structured diff. The
// view decides whether the before/after attribute values come with it — see
// AttributeView. The comparison itself always runs on the full attributes, so
// a redacted diff counts and names exactly the same changes as a full one.
func DiffStates(from, to []byte, view AttributeView) (*StateDiff, error) {
	fromResources, err := ParseResources(from, AttributesFull)
	if err != nil {
		return nil, fmt.Errorf("failed to parse 'from' state: %w", err)
	}
	toResources, err := ParseResources(to, AttributesFull)
	if err != nil {
		return nil, fmt.Errorf("failed to parse 'to' state: %w", err)
	}

	fromMap := make(map[string]Resource, len(fromResources))
	for _, r := range fromResources {
		fromMap[resourceKey(r)] = r
	}

	toMap := make(map[string]Resource, len(toResources))
	for _, r := range toResources {
		toMap[resourceKey(r)] = r
	}

	// Collect all keys
	allKeys := make(map[string]struct{})
	for k := range fromMap {
		allKeys[k] = struct{}{}
	}
	for k := range toMap {
		allKeys[k] = struct{}{}
	}

	sortedKeys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	diff := &StateDiff{}
	for _, key := range sortedKeys {
		fromR, inFrom := fromMap[key]
		toR, inTo := toMap[key]

		switch {
		case !inFrom && inTo:
			after, redacted := view.project(toR.Attributes)
			diff.Added++
			diff.Diffs = append(diff.Diffs, ResourceDiff{
				Type:               toR.Type,
				Name:               toR.Name,
				Module:             toR.Module,
				Action:             "added",
				After:              after,
				AttributesRedacted: redacted,
			})
		case inFrom && !inTo:
			before, redacted := view.project(fromR.Attributes)
			diff.Removed++
			diff.Diffs = append(diff.Diffs, ResourceDiff{
				Type:               fromR.Type,
				Name:               fromR.Name,
				Module:             fromR.Module,
				Action:             "removed",
				Before:             before,
				AttributesRedacted: redacted,
			})
		default:
			changedKeys := findChangedKeys(fromR.Attributes, toR.Attributes)
			if len(changedKeys) > 0 {
				before, redacted := view.project(fromR.Attributes)
				after, _ := view.project(toR.Attributes)
				diff.Changed++
				diff.Diffs = append(diff.Diffs, ResourceDiff{
					Type:               fromR.Type,
					Name:               fromR.Name,
					Module:             fromR.Module,
					Action:             "changed",
					Before:             before,
					After:              after,
					ChangedKeys:        changedKeys,
					AttributesRedacted: redacted,
				})
			} else {
				diff.Unchanged++
			}
		}
	}

	if diff.Diffs == nil {
		diff.Diffs = []ResourceDiff{}
	}
	return diff, nil
}

// findChangedKeys returns the attribute keys that differ between before and after.
func findChangedKeys(before, after map[string]interface{}) []string {
	allKeys := make(map[string]struct{})
	for k := range before {
		allKeys[k] = struct{}{}
	}
	for k := range after {
		allKeys[k] = struct{}{}
	}

	var changed []string
	for k := range allKeys {
		bVal, bOk := before[k]
		aVal, aOk := after[k]

		if !bOk || !aOk {
			changed = append(changed, k)
			continue
		}

		// Normalize through JSON to compare consistently
		bJSON, _ := json.Marshal(bVal)
		aJSON, _ := json.Marshal(aVal)
		if !reflect.DeepEqual(bJSON, aJSON) {
			changed = append(changed, k)
		}
	}

	sort.Strings(changed)
	return changed
}
