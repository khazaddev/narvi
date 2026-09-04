// This file (capability.go) defines the closed capability vocabulary --
// see doc.go for why the two names below are placeholders.

package license

// Capability is the closed vocabulary of paid features a composed module
// may implement and a licence grant may entitle. Closed deliberately:
// adding a value is a reviewable PR to this file, never a string an
// operator or a private module can invent on the fly (a grant naming
// anything outside [All] fails [Parse] with [ErrUnknownCapability], and a
// module declaring anything outside [All] fails controlplane.Build the
// same way) -- see docs/design/boundaries-design.md, section 1.2.
type Capability string

const (
	// CapabilityGovernance is organization-scale review governance --
	// see doc.go for why this name is a placeholder.
	CapabilityGovernance Capability = "governance"

	// CapabilityKnowledgeRetrieval is the private, cross-repository
	// knowledge-retrieval ranking mode (docs/design/boundaries-design.md,
	// section 2, "mode B") -- see doc.go for why this name is a placeholder.
	CapabilityKnowledgeRetrieval Capability = "knowledge-retrieval"
)

// All enumerates every Capability this build defines, in the fixed order
// every consumer that must iterate the full vocabulary (boot-time
// logging, the future GET /api/capabilities read model) uses -- see
// [Parse], which rejects any grant naming a capability not in this list.
var All = []Capability{CapabilityGovernance, CapabilityKnowledgeRetrieval}

// Product is the audience every licence key must carry (the wire
// format's own "product" claim, design note section 1.5) -- a key
// minted for a different Narvi-family product fails [Parse] with
// [ErrWrongProduct] rather than silently entitling capabilities it was
// never meant to.
const Product = "narvi-gatekeeper"
