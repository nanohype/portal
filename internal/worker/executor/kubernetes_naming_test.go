package executor

import (
	"testing"

	"github.com/oklog/ulid/v2"
	"k8s.io/apimachinery/pkg/util/validation"
)

// A run's ConfigMap, Secret and Pod all carry runObjectName as metadata.name.
// The id it is built from is a ULID, and ULID's Crockford base32 alphabet is
// uppercase — which the apiserver rejects for a name, so an unaltered id means
// no run ever reaches tofu.
//
// The rule is asserted with the apiserver's own validator rather than a local
// regexp, so the test tracks the constraint portal is actually held to.
func TestRunObjectName_IsAValidObjectName(t *testing.T) {
	for range 64 {
		id := ulid.Make().String()

		// Positive control. Without it this test passes on any input the
		// validator happens to accept, including a naming scheme that never
		// contained an id at all — and it would have passed unchanged against
		// the construction it exists to rule out.
		if errs := validation.IsDNS1123Subdomain("portal-run-" + id); len(errs) == 0 {
			t.Fatalf("a raw ULID (%s) validated as an object name; this test can no longer detect the defect", id)
		}

		if errs := validation.IsDNS1123Subdomain(runObjectName(id)); len(errs) > 0 {
			t.Errorf("runObjectName(%s) = %s is not a valid object name: %v", id, runObjectName(id), errs)
		}
	}
}

// Lowercasing must not merge two run ids into one name: the ConfigMap name is
// deterministic and a collision would surface as AlreadyExists on the second
// run, or as one run reading the other's payload.
func TestRunObjectName_DistinctIdsStayDistinct(t *testing.T) {
	seen := make(map[string]string, 512)
	for range 512 {
		id := ulid.Make().String()
		name := runObjectName(id)
		if prev, dup := seen[name]; dup {
			t.Fatalf("run ids %s and %s both name %s", prev, id, name)
		}
		seen[name] = id
	}
}
