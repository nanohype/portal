package tenantmanifest

// The parts of the Platform vocabulary portal has to know before it renders
// anything — the values a form offers, and the values a template may cap a
// tenant to.
//
// These are copies of enums the operator's CRD declares, which is a thing worth
// being uncomfortable about: a copy of a schema owned elsewhere goes stale
// silently, and a stale copy here offers an operator a value the apiserver will
// refuse. What makes the copy safe is that it is not trusted — TestVocabulary*
// reads the enum out of the vendored schema beside it and requires this to
// equal it, so the copy cannot drift from the schema without failing the suite,
// and the schema cannot drift from upstream without failing scripts/crd.sh.
//
// The alternative — reading the enum out of the compiled schema at runtime —
// would make every consumer construct a Validator, which loads and compiles
// every CRD, to ask what four strings are.

// ModelFamilies is Platform.spec.identity.allowedModelFamilies.
var ModelFamilies = []string{
	"anthropic",
	"amazon-nova",
	"amazon-titan",
	"meta",
	"mistral",
	"cohere",
	"stability",
}

// IsModelFamily reports whether the CRD's enum admits f.
func IsModelFamily(f string) bool {
	for _, known := range ModelFamilies {
		if known == f {
			return true
		}
	}
	return false
}
