package clusterspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The contract gate.
//
// portal renders a Cluster CR whose schema lives in another repository. Nothing
// in a Go test suite naturally notices when the two disagree — Render round-trips
// through portal's own structs, so a rendered field at the wrong nesting depth
// unmarshals back into the struct it came from and every assertion passes while
// the Kubernetes API server prunes the field on arrival and substitutes a
// default. That is not a loud failure: the vend succeeds, with values nobody
// ordered.
//
// So the schema is vendored (testdata/, digest-pinned — see the README there)
// and checked in two directions:
//
//   - Nothing portal renders may be absent from the schema. This is the pruning
//     check, and it is the one that matters: an unknown field is discarded in
//     silence.
//   - Every field the schema offers must be either expressible through Input or
//     named in unexpressedXRDFields. Not every knob belongs on an order form,
//     but "we chose not to expose this" should be written down rather than
//     indistinguishable from "nobody noticed it existed".

const xrdDir = "testdata"

// unexpressedXRDFields are the Cluster spec fields portal deliberately does not
// put on the order form, each with the reason. Adding a field here is a
// decision; leaving one out is caught by TestEveryXRDFieldIsAccountedFor.
var unexpressedXRDFields = map[string]string{
	"enableAgentPlatform": "always true for a portal vend — the false case installs the operator out of band (the e2e harness, avoiding a GitOps race against a locally-built image), which is not something an order desk does",
	"gitopsRepoUrl":       "the org addon catalog; overriding it points a vended cluster at a fork, which is a fleet-level decision rather than a per-order one",
	"gitopsRepoBranch":    "same as gitopsRepoUrl — main is the catalog's release branch",
	"moduleSource":        "the landing-zone pin is part of the composition↔substrate contract eks-fleet gates in its own CI; a portal order that moved it would vend against an unreviewed substrate",
}

// maximalInput exercises every field portal can express, so the walk below sees
// portal's whole rendered surface rather than whatever a smaller fixture
// happened to include.
func maximalInput() Input {
	yes := true
	n := func(i int) *int { return &i }
	return Input{
		Name: "analytics", Account: "222222222222", Region: "us-east-1", Team: "platform",
		Environment: "production", ClusterVersion: "1.36",
		SystemNodes: &SystemNodes{
			InstanceTypes: []string{"m7g.2xlarge"},
			MinSize:       n(3), MaxSize: n(9), DesiredSize: n(3), DiskSize: n(200),
		},
		Network: &Network{
			Mode: "create",
			Create: &NetworkCreate{
				VpcCidr: defaultVpcCidr, IpamPoolID: "ipam-pool-0abc", IpamNetmaskLength: n(18),
				TransitGatewayID: "tgw-0abc", CentralizedEgress: &yes, MaxAzs: n(3), NatGateways: n(0),
			},
		},
		EndpointPublicAccess:           &yes,
		EndpointPublicAccessCidrs:      []string{"203.0.113.0/24"},
		ObservabilityTier:              "full",
		TTLDays:                        n(7),
		VendRoleArn:                    "arn:aws:iam::222222222222:role/production-eks-fleet-vend",
		DataKmsKeyArn:                  "arn:aws:kms:us-east-1:222222222222:key/abc",
		ClusterPermissionsBoundaryArn:  "arn:aws:iam::222222222222:policy/vend-boundary",
		OperatorPermissionsBoundaryArn: "arn:aws:iam::222222222222:policy/vend-boundary",
		BootstrapAccessRoleArn:         "arn:aws:iam::111111111111:role/eks-fleet-crossplane",
		PortalAccessRoleArn:            "arn:aws:iam::222222222222:role/production-portal-spoke",
		TenantsRepoURL:                 "git@github.com:nanohype/tenants.git",
	}
}

// adoptInput is the other half of the discriminated network union. Create-mode
// coverage alone would leave the adopt branch unrendered and unchecked.
func adoptInput() Input {
	in := maximalInput()
	in.Network = &Network{
		Mode: "adopt",
		Adopt: &NetworkAdopt{
			VpcID: "vpc-0abc",
			SubnetIDs: &NetworkSubnets{
				Private: []string{"subnet-0priv1", "subnet-0priv2"},
				Public:  []string{"subnet-0pub1"},
			},
		},
	}
	return in
}

// specSchema loads the vendored XRD and returns the schema for .spec, after
// verifying the copy has not been edited.
func specSchema(t *testing.T) map[string]any {
	t.Helper()

	manifest := struct {
		Files []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}{}
	raw, err := os.ReadFile(filepath.Join(xrdDir, "source.json"))
	if err != nil {
		t.Fatalf("read source.json: %v", err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse source.json: %v", err)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("source.json lists %d files, want exactly cluster-xrd.yaml", len(manifest.Files))
	}

	// Digest first. Every check below reads meaning out of this file, so a
	// tampered copy would not fail — it would quietly redefine the contract.
	body, err := os.ReadFile(filepath.Join(xrdDir, manifest.Files[0].File))
	if err != nil {
		t.Fatalf("read vendored XRD: %v", err)
	}
	if got := hex.EncodeToString(sha256Sum(body)); got != manifest.Files[0].SHA256 {
		t.Fatalf("vendored XRD digest %s does not match source.json (%s) — re-vendor with `task xrd:sync`, do not hand-edit the copy",
			got, manifest.Files[0].SHA256)
	}

	var xrd struct {
		Spec struct {
			Versions []struct {
				Name   string `json:"name"`
				Schema struct {
					OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &xrd); err != nil {
		t.Fatalf("parse vendored XRD: %v", err)
	}
	for _, v := range xrd.Spec.Versions {
		if v.Name != apiVersionSuffix {
			continue
		}
		props, _ := v.Schema.OpenAPIV3Schema["properties"].(map[string]any)
		spec, ok := props["spec"].(map[string]any)
		if !ok {
			t.Fatalf("XRD version %s has no spec schema", v.Name)
		}
		return spec
	}
	t.Fatalf("vendored XRD has no version %q — portal renders %s", apiVersionSuffix, apiVersion)
	return nil
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// walk reports every path in doc that the schema does not define. It mirrors
// Kubernetes structural-schema pruning: a field the schema does not name is
// dropped on admission, so an unknown path here is a value that never reaches
// the cluster.
func walk(schema, doc map[string]any, path string, unknown *[]string) {
	if schema["x-kubernetes-preserve-unknown-fields"] == true {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	for _, key := range sortedKeys(doc) {
		sub, ok := props[key].(map[string]any)
		if !ok {
			*unknown = append(*unknown, path+"."+key)
			continue
		}
		if child, ok := doc[key].(map[string]any); ok {
			walk(sub, child, path+"."+key, unknown)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renderedSpec renders in and returns its .spec as a generic map.
func renderedSpec(t *testing.T, in Input) map[string]any {
	t.Helper()
	out, err := in.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var cr struct {
		APIVersion string         `json:"apiVersion"`
		Kind       string         `json:"kind"`
		Spec       map[string]any `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(out), &cr); err != nil {
		t.Fatalf("rendered CR is not valid YAML: %v", err)
	}
	if cr.APIVersion != apiVersion || cr.Kind != kind {
		t.Fatalf("rendered %s/%s, want %s/%s", cr.APIVersion, cr.Kind, apiVersion, kind)
	}
	return cr.Spec
}

func TestRenderedCRHasNoFieldTheXRDWouldPrune(t *testing.T) {
	schema := specSchema(t)

	for _, tc := range []struct {
		name string
		in   Input
	}{
		{"create mode", maximalInput()},
		{"adopt mode", adoptInput()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var unknown []string
			walk(schema, renderedSpec(t, tc.in), "spec", &unknown)
			for _, p := range unknown {
				t.Errorf("%s is not in the Cluster XRD: the API server prunes it on admission and substitutes the schema default, so the order silently does not take effect", p)
			}
		})
	}
}

func TestRenderedCRSatisfiesTheXRDRequiredFields(t *testing.T) {
	schema := specSchema(t)
	spec := renderedSpec(t, maximalInput())

	required, _ := schema["required"].([]any)
	if len(required) == 0 {
		t.Fatal("vendored XRD declares no required spec fields — the parse found the wrong node")
	}
	for _, r := range required {
		name, _ := r.(string)
		if _, ok := spec[name]; !ok {
			t.Errorf("spec.%s is required by the XRD and Render omits it", name)
		}
	}
}

func TestRenderedCRRespectsTheXRDEnums(t *testing.T) {
	schema := specSchema(t)
	props, _ := schema["properties"].(map[string]any)

	// Both network modes, so the enum check sees each discriminator value.
	for _, in := range []Input{maximalInput(), adoptInput()} {
		spec := renderedSpec(t, in)
		checked := 0
		for _, key := range sortedKeys(spec) {
			sub, ok := props[key].(map[string]any)
			if !ok {
				continue
			}
			enum, ok := sub["enum"].([]any)
			if !ok {
				continue
			}
			value, ok := spec[key].(string)
			if !ok {
				continue
			}
			checked++
			if !containsString(enum, value) {
				t.Errorf("spec.%s = %q is not in the XRD enum %v", key, value, enum)
			}
		}
		// The fixtures set observabilityTier; a run that checked nothing would
		// pass for the wrong reason.
		if checked == 0 {
			t.Error("no enum-constrained field was checked — the fixture stopped setting one")
		}
	}
}

// TestEveryXRDFieldIsAccountedFor is the half that catches a *new* upstream
// field. The pruning check above only sees what portal already renders, so it
// stays green forever after eks-fleet adds a knob nobody wired up — which is
// precisely how observabilityTier and tenantsRepoUrl went unnoticed.
func TestEveryXRDFieldIsAccountedFor(t *testing.T) {
	schema := specSchema(t)
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatal("vendored XRD spec has no properties — the parse found the wrong node")
	}

	rendered := renderedSpec(t, maximalInput())
	for _, field := range sortedKeys(props) {
		_, expressed := rendered[field]
		reason, declared := unexpressedXRDFields[field]
		switch {
		case expressed && declared:
			t.Errorf("spec.%s is both rendered and listed as unexpressed (%q) — one of the two is stale", field, reason)
		case !expressed && !declared:
			t.Errorf("spec.%s exists in the Cluster XRD but portal neither renders it nor lists it in unexpressedXRDFields; decide which and write it down", field)
		}
	}
	for field := range unexpressedXRDFields {
		if _, ok := props[field]; !ok {
			t.Errorf("unexpressedXRDFields names spec.%s, which the XRD no longer has — drop the entry", field)
		}
	}
}

// celRule is one x-kubernetes-validations entry in the vendored schema and the
// portal code that mirrors it.
type celRule struct {
	path, rule, mirror string
}

// mirroredCELRules is the tripwire under Validate.
//
// The schema is vendored for its *structure* — properties, required, enums —
// and the contract tests above compare the render against it. Its *rules* are a
// different thing: portal hand-copies each one so a bad order is a 400 on the
// form rather than an admission failure that lands after the manifest is
// committed and the operation reported committed. Hand-copies drift, and this
// pair would drift the same silent way the render did.
//
// This does not prove a mirror is correct — nothing here evaluates CEL. It
// proves the rule set has not moved underneath the mirrors. A tightened,
// removed or added rule fails on the re-vendor, which is the moment someone is
// already reading the schema diff and can decide what it means.
var mirroredCELRules = []celRule{
	{
		path:   "spec",
		rule:   `self.clusterName != self.environment`,
		mirror: "Validate: name must not equal environment (the cluster name would double)",
	},
	{
		path:   "spec",
		rule:   `!has(self.endpointPublicAccess) || !self.endpointPublicAccess || (has(self.endpointPublicAccessCidrs) && self.endpointPublicAccessCidrs.size() > 0)`,
		mirror: "Validate: a public endpoint requires a CIDR allowlist",
	},
	{
		path:   "spec",
		rule:   `!has(self.vendRoleArn) || self.vendRoleArn == "" || (has(self.clusterPermissionsBoundaryArn) && self.clusterPermissionsBoundaryArn != "" && has(self.operatorPermissionsBoundaryArn) && self.operatorPermissionsBoundaryArn != "")`,
		mirror: "Validate: a cross-account vend requires both permissions boundaries",
	},
	{
		path:   "spec.network",
		rule:   `self.mode != 'adopt' || (has(self.adopt) && self.adopt.vpcId != '' && has(self.adopt.subnetIds) && has(self.adopt.subnetIds.private) && self.adopt.subnetIds.private.size() > 0)`,
		mirror: "Network.validate: adopt requires a VPC and non-empty private subnets",
	},
	{
		path:   "spec.network",
		rule:   `!has(self.create) || !has(self.create.centralizedEgress) || !self.create.centralizedEgress || (has(self.create.transitGatewayId) && self.create.transitGatewayId != '')`,
		mirror: "Network.validate: centralized egress requires a transit gateway",
	},
	{
		path:   "spec.network",
		rule:   `!has(self.create) || !has(self.create.ipamPoolId) || self.create.ipamPoolId == '' || !has(self.create.vpcCidr) || self.create.vpcCidr == '10.0.0.0/16'`,
		mirror: "Network.validate: an IPAM pool and a chosen CIDR are mutually exclusive",
	},
}

// collectCELRules walks the schema and returns every validation rule by path,
// with whitespace normalized — YAML block folding is a formatting choice, and a
// re-wrapped rule is not a changed one.
func collectCELRules(schema map[string]any, path string, into map[string]bool) {
	rules, _ := schema["x-kubernetes-validations"].([]any)
	for _, entry := range rules {
		e, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rule, _ := e["rule"].(string)
		into[celKey(path, rule)] = true
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, key := range sortedKeys(props) {
			if sub, ok := props[key].(map[string]any); ok {
				collectCELRules(sub, path+"."+key, into)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		collectCELRules(items, path+"[]", into)
	}
}

func celKey(path, rule string) string {
	return path + ": " + strings.Join(strings.Fields(rule), " ")
}

func TestEveryXRDValidationRuleHasAMirror(t *testing.T) {
	found := map[string]bool{}
	collectCELRules(specSchema(t), "spec", found)

	// A collector that walked nothing would make both loops below pass
	// vacuously, which is the one way this tripwire could fail open.
	if len(found) < len(mirroredCELRules) {
		t.Fatalf("found %d validation rules in the vendored XRD but %d are recorded — the collector stopped walking", len(found), len(mirroredCELRules))
	}

	recorded := map[string]celRule{}
	for _, r := range mirroredCELRules {
		recorded[celKey(r.path, r.rule)] = r
	}

	for key := range found {
		if _, ok := recorded[key]; !ok {
			t.Errorf("the Cluster XRD enforces a rule portal does not mirror:\n  %s\nEither mirror it in Validate — a rule broken at admission surfaces after the manifest is committed and the operation reported committed — or record it here with a note saying why portal does not.", key)
		}
	}
	for key, r := range recorded {
		if !found[key] {
			t.Errorf("portal mirrors a rule the Cluster XRD no longer states as recorded:\n  %s\nmirrored by %s\nThe schema was re-vendored and this rule changed or went away; re-read it and update the mirror.", key, r.mirror)
		}
	}
}

func containsString(haystack []any, needle string) bool {
	for _, h := range haystack {
		if s, ok := h.(string); ok && s == needle {
			return true
		}
	}
	return false
}

func TestVendoredXRDMatchesTheAPIVersionPortalRenders(t *testing.T) {
	// specSchema fatals when the vendored XRD carries no version matching
	// apiVersion, so reaching here is the assertion. Named separately because a
	// version bump upstream is a different fix from a field change.
	if got := fmt.Sprintf("fleet.nanohype.dev/%s", apiVersionSuffix); got != apiVersion {
		t.Fatalf("apiVersionSuffix %q does not compose apiVersion %q", apiVersionSuffix, apiVersion)
	}
	specSchema(t)
}
