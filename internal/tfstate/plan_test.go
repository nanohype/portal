package tfstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The goldens are two halves of one real OpenTofu plan: what `tofu show -json`
// stored and what `tofu show` rendered. See testdata/regenerate.sh for what each
// sentinel and each resource is there to prove.
func loadGoldens(t *testing.T) (planJSON []byte, planText string) {
	t.Helper()

	planJSON, err := os.ReadFile(filepath.Join("testdata", "plan.json"))
	if err != nil {
		t.Fatalf("reading the plan golden: %v", err)
	}
	text, err := os.ReadFile(filepath.Join("testdata", "plan.txt"))
	if err != nil {
		t.Fatalf("reading the rendered-plan golden: %v", err)
	}
	return planJSON, string(text)
}

func projectGolden(t *testing.T, view AttributeView) (*Plan, string) {
	t.Helper()

	planJSON, _ := loadGoldens(t)
	plan, err := ProjectPlan(planJSON, view)
	if err != nil {
		t.Fatalf("projecting the plan: %v", err)
	}
	served, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshalling the projection: %v", err)
	}
	return plan, string(served)
}

// Every sentinel in the fixture, and whether OpenTofu's own text render shows it.
//
// This is the whole contract in one table. A value the text withholds must not
// survive the projection; a value the text prints may, because the same caller
// reads that text in the run log and on the run's detail response, so withholding
// it here would buy nothing.
//
// The inText column is asserted against the golden rather than trusted, so a
// regenerated fixture that stops exercising a case fails here instead of quietly
// making the test weaker.
var sentinels = []struct {
	value  string
	inText bool
	why    string
}{
	{
		value:  "sentinel-rotated-FFFF",
		inText: false,
		why:    "the incoming sensitive value, marked sensitive everywhere it appears in resource_changes and unmarked in .variables",
	},
	{
		value:  "sentinel-plain-BBBB",
		inText: false,
		why:    "a variable reachable only through .variables, .planned_values, .output_changes, .prior_state and .configuration",
	},
	{
		value:  "sentinel-no-op-EEEE",
		inText: false,
		why:    "an attribute of a resource the plan is not touching, carried by a no-op entry",
	},
	{
		value:  "sentinel-var-default-AAAA",
		inText: true,
		why:    "the outgoing secret, which tofu itself prints out of the unmarked computed attribute",
	},
	{
		value:  "sentinel-unchanged-CCCC",
		inText: true,
		why:    "a member that did not change inside a map attribute that did",
	},
	{
		value:  "sentinel-list-plain-DDDD",
		inText: true,
		why:    "a non-sensitive value inside a list",
	},
}

// The fixture exercises what it claims to.
//
// Without this the table above is just an assertion about itself: a golden that
// drifted until every sentinel appeared in the text would make the disclosure
// test below pass while testing nothing.
func TestTheGoldenPlansStillExerciseEveryCase(t *testing.T) {
	planJSON, planText := loadGoldens(t)

	var withheld int
	for _, s := range sentinels {
		got := strings.Contains(planText, s.value)
		if got != s.inText {
			t.Errorf("%s: rendered plan shows it = %v, table says %v — the fixture has drifted from what it documents (%s)",
				s.value, got, s.inText, s.why)
		}
		if !s.inText {
			withheld++
		}
	}
	if withheld < 3 {
		t.Fatalf("only %d sentinels are withheld by the text render; the fixture no longer exercises the leak", withheld)
	}

	// The field names the redaction reads. If a later tofu renames them, every
	// mirror unmarshals to nil — which now withholds rather than discloses, but
	// silently and completely, and this is what says so out loud.
	for _, field := range []string{`"before_sensitive"`, `"after_sensitive"`} {
		if !strings.Contains(string(planJSON), field) {
			t.Errorf("the golden carries no %s; the field the redaction reads has been renamed or removed", field)
		}
	}

	var probe struct {
		TerraformVersion string `json:"terraform_version"`
	}
	if err := json.Unmarshal(planJSON, &probe); err != nil {
		t.Fatalf("parsing the golden: %v", err)
	}
	if probe.TerraformVersion == "" {
		t.Error("the golden does not say which tofu produced it")
	}
}

// Nothing the rendered plan withholds survives into what portal serves.
//
// This is the defect. `tofu show -json` does not redact: it emits the cleartext
// of every value the text render replaces with "(sensitive value)", and marks
// sensitivity in a structure alongside. It also carries whole sections the text
// has no equivalent for — the root variables, the prior state, the configuration
// — and the endpoint used to write those bytes to the response verbatim, at the
// workspace read bar.
func TestTheServedPlanWithholdsEverythingTheRenderedPlanWithholds(t *testing.T) {
	_, served := projectGolden(t, AttributesRedacted)

	for _, s := range sentinels {
		if s.inText {
			continue
		}
		if strings.Contains(served, s.value) {
			t.Errorf("%s reached the response: %s", s.value, s.why)
		}
	}
}

// The same, stated over values rather than over a list somebody has to maintain.
//
// Every string the projection puts in an attribute position has to appear in the
// block the rendered plan printed FOR THAT RESOURCE. Matching the whole document
// would be too weak: a value tofu prints for one resource and hides on another
// would pass, and in a fixture built only of resources that echo their input
// into a computed attribute, everything passes.
//
// This one does not know what a secret looks like, so a field added to the
// projection later — or a redaction that stops firing — is caught without anyone
// remembering to add a sentinel for it.
func TestEveryValueServedAppearsInTheRenderedPlanForItsOwnResource(t *testing.T) {
	plan, _ := projectGolden(t, AttributesRedacted)
	_, planText := loadGoldens(t)
	blocks := renderedBlocks(t, planText)

	var evidence int
	for _, rc := range plan.ResourceChanges {
		block, ok := blocks[rc.Address]
		if !ok {
			t.Errorf("%s was served but the rendered plan has no block for it", rc.Address)
			continue
		}
		for _, side := range []map[string]any{rc.Change.Before, rc.Change.After} {
			for _, leaf := range stringLeaves(side) {
				if !strings.Contains(block, leaf) {
					t.Errorf("%s: served the value %q, which the rendered plan does not print for that resource",
						rc.Address, leaf)
				}
				// The marker is in every block by construction, and one-character
				// values are in any text. Neither is evidence of anything.
				if leaf != sensitiveValue && len(leaf) >= 8 {
					evidence++
				}
			}
		}
	}

	if evidence < 8 {
		t.Fatalf("only %d substantial values were checked; the projection is not returning enough to be testing anything", evidence)
	}
}

// renderedBlocks splits `tofu show` output into the per-resource blocks it
// prints, keyed by address. The header is the same one the UI parses.
func renderedBlocks(t *testing.T, planText string) map[string]string {
	t.Helper()

	header := regexp.MustCompile(`^\s*# (\S+) (?:will|must) be `)
	blocks := map[string]string{}

	current := ""
	var buf []string
	flush := func() {
		if current != "" {
			blocks[current] = strings.Join(buf, "\n")
		}
	}
	for _, line := range strings.Split(planText, "\n") {
		if m := header.FindStringSubmatch(line); m != nil {
			flush()
			current, buf = m[1], nil
		}
		buf = append(buf, line)
	}
	flush()

	if len(blocks) < 4 {
		t.Fatalf("only found %d resource blocks in the rendered plan; the split is not working", len(blocks))
	}
	return blocks
}

// A caller who may read this workspace's state gets the values.
//
// The negative half of the redaction: without it, a projection that dropped
// every value at every bar would pass the disclosure tests above completely.
func TestTheFullViewServesTheValuesTheRedactedViewWithholds(t *testing.T) {
	_, served := projectGolden(t, AttributesFull)

	if !strings.Contains(served, "sentinel-rotated-FFFF") {
		t.Error("the full view withheld the sensitive value; then no bar serves it and the view is decorative")
	}
	if strings.Contains(served, sensitiveValue) {
		t.Error("the full view emitted the redaction marker")
	}

	// Still a projection: the bar decides values, not sections.
	for _, gone := range []string{"sentinel-plain-BBBB", "sentinel-no-op-EEEE"} {
		if strings.Contains(served, gone) {
			t.Errorf("%s survived at the full view; dropping .variables/.configuration/.prior_state and no-op entries is the contract, not a redaction", gone)
		}
	}
}

// A rotated secret is still reported as a change.
//
// Both sides redact to the same marker, so anything downstream that recovered
// "what changed" by comparing the two maps would decide a rotation was a no-op —
// a resource updating with nothing in it. Narrowing runs on the real values
// first so that every key present is one that moved.
func TestARotatedSecretIsStillReportedAsAChange(t *testing.T) {
	plan, _ := projectGolden(t, AttributesRedacted)

	rc := findChange(t, plan, "terraform_data.rotated")
	before, ok := rc.Change.Before["input"].(map[string]any)
	if !ok {
		t.Fatal("the attribute holding the rotated secret is missing from before")
	}
	after, ok := rc.Change.After["input"].(map[string]any)
	if !ok {
		t.Fatal("the attribute holding the rotated secret is missing from after")
	}
	if before["password"] != sensitiveValue || after["password"] != sensitiveValue {
		t.Errorf("the rotated secret was not redacted on both sides: before=%v after=%v",
			before["password"], after["password"])
	}
}

// The same property, stated where the golden cannot state it.
//
// terraform_data always carries a computed `output`, so on the fixture a changed
// resource differs by that key whatever happens to the one under test. This is
// the pure case: a change whose ONLY moving attribute is sensitive still has to
// arrive as a change, or the UI renders an update with nothing in it and a
// reviewer approves a rotation they never saw.
func TestAChangeWhoseOnlyMovementIsSensitiveIsStillAChange(t *testing.T) {
	doc := []byte(`{"format_version":"1.2","resource_changes":[{
	  "address":"aws_db_instance.only","mode":"managed","type":"aws_db_instance",
	  "name":"only","provider_name":"registry.opentofu.org/hashicorp/aws",
	  "change":{"actions":["update"],
	    "before":{"password":"old-secret","name":"db"},
	    "after":{"password":"new-secret","name":"db"},
	    "before_sensitive":{"password":true},"after_sensitive":{"password":true}}}]}`)

	plan, err := ProjectPlan(doc, AttributesRedacted)
	if err != nil {
		t.Fatalf("projecting: %v", err)
	}
	rc := findChange(t, plan, "aws_db_instance.only")

	if _, ok := rc.Change.Before["password"]; !ok {
		t.Error("the only attribute that moved is absent from before")
	}
	if _, ok := rc.Change.After["password"]; !ok {
		t.Error("the only attribute that moved is absent from after")
	}
	if _, ok := rc.Change.Before["name"]; ok {
		t.Error("an attribute that did not move was sent")
	}
	if rc.Change.Before["password"] != sensitiveValue || rc.Change.After["password"] != sensitiveValue {
		t.Error("the secret was not withheld")
	}
}

// Each side is redacted against its own mirror.
//
// The golden cannot catch this: every resource in it whose mirrors differ does
// so only in a computed attribute, so swapping the two arguments moves nothing
// the disclosure tests can see. Stated on synthetic input, where the two mirrors
// mark different keys, swapping them is immediate.
func TestEachSideIsRedactedAgainstItsOwnMirror(t *testing.T) {
	doc := []byte(`{"format_version":"1.2","resource_changes":[{
	  "address":"aws_x.y","mode":"managed","type":"aws_x","name":"y","provider_name":"p",
	  "change":{"actions":["update"],
	    "before":{"x":"before-x","y":"before-y"},
	    "after":{"x":"after-x","y":"after-y"},
	    "before_sensitive":{"x":true},
	    "after_sensitive":{"y":true}}}]}`)

	plan, err := ProjectPlan(doc, AttributesRedacted)
	if err != nil {
		t.Fatalf("projecting: %v", err)
	}
	rc := findChange(t, plan, "aws_x.y")

	for _, tt := range []struct {
		where string
		got   any
		want  any
	}{
		{"before.x (marked on the before side)", rc.Change.Before["x"], sensitiveValue},
		{"before.y (not marked on the before side)", rc.Change.Before["y"], "before-y"},
		{"after.y (marked on the after side)", rc.Change.After["y"], sensitiveValue},
		{"after.x (not marked on the after side)", rc.Change.After["x"], "after-x"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.where, tt.got, tt.want)
		}
	}
}

// A change with no sensitivity mirror at all withholds everything.
//
// THE FAIL-OPEN THIS CLOSES: an absent mirror is not a claim that nothing is
// sensitive, it is the absence of any claim. Reading it as "nothing is
// sensitive" served every value in the change in cleartext at the workspace read
// bar, with sensitive_values_redacted:true set alongside saying otherwise —
// which is worse than not claiming redaction at all.
func TestAChangeWithNoSensitivityMirrorWithholdsEverything(t *testing.T) {
	doc := []byte(`{"format_version":"1.2","resource_changes":[{
	  "address":"aws_db_instance.x","mode":"managed","type":"aws_db_instance",
	  "name":"x","provider_name":"p",
	  "change":{"actions":["update"],
	    "before":{"password":"hunter2"},
	    "after":{"password":"hunter3"}}}]}`)

	plan, err := ProjectPlan(doc, AttributesRedacted)
	if err != nil {
		t.Fatalf("projecting: %v", err)
	}
	served, _ := json.Marshal(plan)
	if strings.Contains(string(served), "hunter") {
		t.Errorf("a change with no mirror was served in cleartext: %s", served)
	}

	rc := findChange(t, plan, "aws_db_instance.x")
	if rc.Change.Before["password"] != sensitiveValue {
		t.Errorf("before.password = %v, want the marker", rc.Change.Before["password"])
	}
	// The names stay: they are what the diff is made of, and they are not the secret.
	if _, ok := rc.Change.After["password"]; !ok {
		t.Error("the attribute name was dropped along with the value")
	}
}

// An explicit false is tofu saying nothing here is sensitive, and is honoured.
//
// The other half of the rule above. Without this, withholding on an absent
// mirror could be implemented as withholding on anything that is not a mirror
// object, and every create in every plan would come back fully redacted —
// before_sensitive is the bare bool false on a create.
func TestAnExplicitlyUnmarkedSideKeepsItsValues(t *testing.T) {
	doc := []byte(`{"format_version":"1.2","resource_changes":[{
	  "address":"aws_x.y","mode":"managed","type":"aws_x","name":"y","provider_name":"p",
	  "change":{"actions":["create"],
	    "before":null,"after":{"name":"visible"},
	    "before_sensitive":false,"after_sensitive":false}}]}`)

	plan, err := ProjectPlan(doc, AttributesRedacted)
	if err != nil {
		t.Fatalf("projecting: %v", err)
	}
	rc := findChange(t, plan, "aws_x.y")
	if rc.Change.After["name"] != "visible" {
		t.Errorf("after.name = %v, want the value — false is a marking, not the absence of one", rc.Change.After["name"])
	}
	if rc.Change.Before != nil {
		t.Errorf("before = %v, want nil so the UI reads it as a create", rc.Change.Before)
	}
}

// Attributes that did not change are not sent — down to the member.
//
// The plan's before/after are the resource's complete attribute maps, so an edit
// to one attribute shipped every other attribute of that resource with it, and
// an edit to one member of a map attribute shipped every sibling member. The
// rendered plan hides both: whole attributes it omits, members it replaces with
// "(N unchanged attributes hidden)". A sibling is where an unmarked credential
// sits — a config map's other keys, a settings block's other fields.
func TestUnchangedAttributesAreNotSentDownToTheMember(t *testing.T) {
	plan, _ := projectGolden(t, AttributesRedacted)

	rc := findChange(t, plan, "terraform_data.rotated")

	// The whole attribute: id does not change on an in-place update.
	if _, ok := rc.Change.Before["id"]; ok {
		t.Error("id did not change and was sent anyway")
	}
	if _, ok := rc.Change.After["id"]; ok {
		t.Error("id did not change and was sent anyway")
	}

	// The member: `input` changed because `password` did, and `untouched` rode
	// along inside it.
	before, ok := rc.Change.Before["input"].(map[string]any)
	if !ok {
		t.Fatal("the changed map attribute is missing from before; the fixture no longer exercises member narrowing")
	}
	if _, ok := before["untouched"]; ok {
		t.Error("a member that did not change was sent inside the attribute that did")
	}
	if _, ok := before["password"]; !ok {
		t.Fatal("the member that DID change was dropped; narrowing has gone too far")
	}
}

// Resources the plan is not touching are not sent at all.
//
// They are most of the document, they each carry a full attribute map, the text
// render does not mention them, and the only thing reading this already throws
// them away.
func TestNoOpResourcesAreDropped(t *testing.T) {
	plan, _ := projectGolden(t, AttributesRedacted)

	for _, rc := range plan.ResourceChanges {
		if isNoOp(rc.Change.Actions) {
			t.Errorf("%s is a no-op and was served anyway", rc.Address)
		}
	}
	if len(plan.ResourceChanges) == 0 {
		t.Fatal("every change was dropped")
	}

	// The fixture holds one, or this proves nothing.
	var raw rawPlan
	planJSON, _ := loadGoldens(t)
	if err := json.Unmarshal(planJSON, &raw); err != nil {
		t.Fatalf("parsing the golden: %v", err)
	}
	var noOps int
	for _, rc := range raw.ResourceChanges {
		if isNoOp(rc.Change.Actions) {
			noOps++
		}
	}
	if noOps == 0 {
		t.Fatal("the golden holds no no-op entry, so dropping them is untested")
	}
}

// A resource inside a module keeps the address that says so.
func TestModuleResourcesKeepTheirModuleAddress(t *testing.T) {
	plan, _ := projectGolden(t, AttributesRedacted)

	rc := findChange(t, plan, "module.child.terraform_data.inner")
	if rc.ModuleAddress != "module.child" {
		t.Errorf("module_address = %q, want module.child", rc.ModuleAddress)
	}
	input, ok := rc.Change.After["input"].(map[string]any)
	if !ok {
		t.Fatal("the module resource's changed attribute is missing")
	}
	if input["held"] != sensitiveValue {
		t.Errorf("a sensitive value inside a module was served as %v", input["held"])
	}
}

// Sections the projection does not name never reach the response.
//
// The allowlist is the struct definition — a field with nowhere to land is
// dropped by encoding/json before any of this code runs. This holds that
// property against a document carrying sections that do not exist yet.
func TestSectionsTheProjectionDoesNotNameAreDropped(t *testing.T) {
	invented := []byte(`{
	  "format_version": "1.2",
	  "variables": {"tok": {"value": "leak-via-variables"}},
	  "prior_state": {"values": {"outputs": {"o": {"value": "leak-via-prior-state"}}}},
	  "configuration": {"root_module": {"variables": {"v": {"default": "leak-via-configuration"}}}},
	  "planned_values": {"root_module": {"resources": [{"values": {"a": "leak-via-planned-values"}}]}},
	  "output_changes": {"o": {"after": "leak-via-output-changes"}},
	  "resource_drift": [{"change": {"after": {"a": "leak-via-drift"}}}],
	  "checks": [{"problems": [{"message": "leak-via-checks"}]}],
	  "some_field_tofu_has_not_invented_yet": {"nested": "leak-via-the-future"},
	  "resource_changes": [
	    {
	      "address": "terraform_data.x", "mode": "managed", "type": "terraform_data",
	      "name": "x", "provider_name": "terraform.io/builtin/terraform",
	      "change": {
	        "actions": ["update"],
	        "before": {"keep": "before-value"},
	        "after": {"keep": "after-value"},
	        "before_sensitive": {}, "after_sensitive": {},
	        "importing": {"id": "leak-via-importing"},
	        "replace_paths": [["leak-via-replace-paths"]]
	      }
	    }
	  ]
	}`)

	plan, err := ProjectPlan(invented, AttributesRedacted)
	if err != nil {
		t.Fatalf("projecting: %v", err)
	}
	served, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if !strings.Contains(string(served), "after-value") {
		t.Fatal("the change itself was dropped, so this proves nothing")
	}
	for _, leak := range []string{
		"leak-via-variables", "leak-via-prior-state", "leak-via-configuration",
		"leak-via-planned-values", "leak-via-output-changes", "leak-via-drift",
		"leak-via-checks", "leak-via-the-future", "leak-via-importing",
		"leak-via-replace-paths",
	} {
		if strings.Contains(string(served), leak) {
			t.Errorf("%s reached the response", leak)
		}
	}
}

// The mirror is not always shaped like the value it describes.
//
// These are the shapes a real plan ships, taken from the golden: a bare false on
// the empty side of a create, a bare true for a whole subtree, an object, a list,
// and a list that runs out before the value list does.
func TestRedactionFollowsEveryShapeTheMirrorTakes(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		mirror any
		want   any
	}{
		{
			name:   "a bare false marks nothing",
			value:  map[string]any{"a": "keep"},
			mirror: false,
			want:   map[string]any{"a": "keep"},
		},
		{
			name:   "a bare true marks the whole subtree",
			value:  map[string]any{"a": "hide", "b": "hide"},
			mirror: true,
			want:   sensitiveValue,
		},
		{
			name:   "an object mirror marks named members",
			value:  map[string]any{"a": "hide", "b": "keep"},
			mirror: map[string]any{"a": true},
			want:   map[string]any{"a": sensitiveValue, "b": "keep"},
		},
		{
			name:   "a member marked false is kept",
			value:  map[string]any{"a": "keep"},
			mirror: map[string]any{"a": false},
			want:   map[string]any{"a": "keep"},
		},
		{
			name:   "a list mirror marks by position",
			value:  []any{"hide", "keep"},
			mirror: []any{true, false},
			want:   []any{sensitiveValue, "keep"},
		},
		{
			name:   "nesting is followed to the leaf",
			value:  map[string]any{"l": []any{map[string]any{"s": "hide", "n": "keep"}}},
			mirror: map[string]any{"l": []any{map[string]any{"s": true}}},
			want:   map[string]any{"l": []any{map[string]any{"s": sensitiveValue, "n": "keep"}}},
		},
		{
			name:   "a mirror that runs out withholds the rest",
			value:  []any{"keep", "unknown", "unknown"},
			mirror: []any{false},
			want:   []any{"keep", sensitiveValue, sensitiveValue},
		},
		{
			name:   "a mirror that disagrees about the shape withholds",
			value:  "scalar",
			mirror: map[string]any{"a": true},
			want:   sensitiveValue,
		},
		{
			name:   "a list mirror over a scalar withholds",
			value:  "scalar",
			mirror: []any{true},
			want:   sensitiveValue,
		},
		{
			name:   "a null value stays null",
			value:  nil,
			mirror: true,
			want:   nil,
		},
		{
			// THE FAIL-OPEN: this used to return the value. An absent marking is
			// not a marking of false.
			name:   "an absent mirror withholds",
			value:  map[string]any{"a": "hide"},
			mirror: nil,
			want:   sensitiveValue,
		},
		{
			name:   "a null where a marking should be withholds",
			value:  map[string]any{"a": "hide"},
			mirror: map[string]any{"a": nil},
			want:   map[string]any{"a": sensitiveValue},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactValue(tt.value, tt.mirror); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("redactValue(%#v, %#v) = %#v, want %#v", tt.value, tt.mirror, got, tt.want)
			}
		})
	}
}

// A whole side withheld keeps its attribute names.
//
// redactValue collapses a subtree to one marker, which at the top of a change
// would replace the attribute map with a string and lose the names the diff is
// made of. The names are not the secret.
func TestAWhollyWithheldChangeKeepsItsAttributeNames(t *testing.T) {
	want := map[string]any{"a": sensitiveValue, "b": sensitiveValue}

	for _, mirror := range []any{true, nil} {
		got := redactMarked(map[string]any{"a": "hide", "b": "hide"}, mirror)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("redactMarked(_, %#v) = %#v, want %#v", mirror, got, want)
		}
	}
}

// Malformed input is an error, not a partial plan.
func TestUnparseablePlansAreRefused(t *testing.T) {
	if _, err := ProjectPlan([]byte("not json"), AttributesRedacted); err == nil {
		t.Error("accepted a plan that is not JSON")
	}
}

// The zero value of the view redacts, so a caller that never decides discloses
// nothing. AttributesRedacted is iota; this holds that it stays that way.
func TestTheZeroViewRedacts(t *testing.T) {
	planJSON, _ := loadGoldens(t)

	var view AttributeView
	plan, err := ProjectPlan(planJSON, view)
	if err != nil {
		t.Fatalf("projecting: %v", err)
	}
	if !plan.SensitiveValuesRedacted {
		t.Fatal("the zero view served values")
	}
	served, _ := json.Marshal(plan)
	if strings.Contains(string(served), "sentinel-rotated-FFFF") {
		t.Error("the zero view disclosed the sensitive value")
	}
}

func findChange(t *testing.T, plan *Plan, address string) ResourceChange {
	t.Helper()
	for _, rc := range plan.ResourceChanges {
		if rc.Address == address {
			return rc
		}
	}
	t.Fatalf("%s is not in the projection; the golden has changed", address)
	return ResourceChange{}
}

// stringLeaves collects every string a value holds, at any depth.
func stringLeaves(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case map[string]any:
		var out []string
		for _, sub := range t {
			out = append(out, stringLeaves(sub)...)
		}
		return out
	case []any:
		var out []string
		for _, sub := range t {
			out = append(out, stringLeaves(sub)...)
		}
		return out
	default:
		return nil
	}
}
