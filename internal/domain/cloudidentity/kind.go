package cloudidentity

// Kind is one of the 4 recognized cloud/identity-consumer families §27.3
// names -- matches the Postgres cloud_identity_binding_kind ENUM
// (migrations/000093_cloud_identity_bindings.up.sql) verbatim.
type Kind string

// The 4 recognized Kind values -- see AllKinds below for the same set as
// a ranged-over slice.
const (
	KindAWS     Kind = "aws"
	KindGCP     Kind = "gcp"
	KindAzure   Kind = "azure"
	KindGeneric Kind = "generic"
)

// AllKinds is every recognized Kind, in this file's own declaration
// order -- exported so a caller (e.g. a test ranging exhaustively) never
// needs to hand-maintain a second list.
var AllKinds = []Kind{KindAWS, KindGCP, KindAzure, KindGeneric}

// IsValidKind reports whether k is one of the 4 recognized Kind values.
func IsValidKind(k Kind) bool {
	switch k {
	case KindAWS, KindGCP, KindAzure, KindGeneric:
		return true
	}
	return false
}
