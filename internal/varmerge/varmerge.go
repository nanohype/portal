// Package varmerge holds the variable-precedence rules shared by the worker
// and the API.
//
// Variables resolve org → pipeline → workspace, highest wins, except that tag
// variables deep-merge as JSON maps instead of replacing. Two places need that
// rule and need to agree on it: the worker, which computes the values a run
// actually executes with, and the API's effective-variables view, which is what
// an operator reads before approving a production apply.
//
// Both consumers apply a layer through Layer, so the rule cannot drift between
// them: a tag suffix added here changes what the run uses and what the view
// shows in the same edit. Two implementations of it would not disagree loudly —
// they would leave the UI showing an effective set the run does not use, with
// no error anywhere.
package varmerge

import (
	"encoding/json"
	"strings"
)

// IsTagsKey reports whether a variable deep-merges rather than replaces.
//
// The suffix rule is deliberately broad: infrastructure code names tag maps
// many ways (`default_tags`, `common_tags`, `eks_tags`), and a tag map that
// replaced instead of merging would silently drop the org's tags from every
// resource in a workspace that set its own.
func IsTagsKey(key string) bool {
	return key == "tags" || key == "default_tags" || key == "extra_tags" ||
		strings.HasSuffix(key, "_tags")
}

// DeepMergeJSON merges two JSON object strings, keys in b overriding keys in a.
//
// Returns "" when either side is not a JSON object, which callers treat as "no
// merge" and fall back to plain replacement. That is the right failure: a tag
// variable holding a non-JSON value is a user error, and refusing to merge it
// leaves the higher-precedence value intact rather than inventing one.
func DeepMergeJSON(a, b string) string {
	var mapA, mapB map[string]interface{}
	if json.Unmarshal([]byte(a), &mapA) != nil {
		return ""
	}
	if json.Unmarshal([]byte(b), &mapB) != nil {
		return ""
	}
	for k, v := range mapB {
		mapA[k] = v
	}
	out, err := json.Marshal(mapA)
	if err != nil { //coverage:ignore — every value in mapA came out of json.Unmarshal above, so it is already a marshalable type; there is no input that reaches this.
		return ""
	}
	return string(out)
}

// Layer returns the value a merged variable set should carry for key once
// incoming arrives on top of what is already there. exists reports whether a
// lower-precedence layer already set key.
//
// Tag maps deep-merge; everything else is replaced by the higher scope.
//
// There is deliberately no sensitivity check. The effective view redacts before
// it layers, so a sensitive tag map arrives as a redaction rather than an
// object, DeepMergeJSON declines it, and the higher scope's value stands —
// which is the right answer. Composing two redactions into one invented value
// would be worse than showing the one that wins.
func Layer(key, category, existing, incoming string, exists bool) string {
	if exists && category == "terraform" && IsTagsKey(key) {
		if merged := DeepMergeJSON(existing, incoming); merged != "" {
			return merged
		}
	}
	return incoming
}
