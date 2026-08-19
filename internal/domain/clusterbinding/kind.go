package clusterbinding

// AuthKind is one of the 3 auth rungs §27.4 names -- matches the Postgres
// cluster_binding_auth_kind ENUM (migrations/
// 000094_cluster_bindings.up.sql) verbatim. See this package's own doc.go
// for the full "what each rung needs" writeup.
type AuthKind string

// The 3 recognized AuthKind values, in §27.4's OWN preference order
// (cloud > oidc > static, "preferring federation over static material") --
// see AllAuthKinds below for the same set as a ranged-over slice, and
// PreferenceRank for that ordering expressed numerically.
const (
	AuthKindCloud  AuthKind = "cloud"
	AuthKindOIDC   AuthKind = "oidc"
	AuthKindStatic AuthKind = "static"
)

// AllAuthKinds is every recognized AuthKind, in this file's own
// declaration order (== §27.4's own preference order) -- exported so a
// caller (e.g. a test ranging exhaustively) never needs to hand-maintain
// a second list.
var AllAuthKinds = []AuthKind{AuthKindCloud, AuthKindOIDC, AuthKindStatic}

// IsValidAuthKind reports whether k is one of the 3 recognized AuthKind
// values.
func IsValidAuthKind(k AuthKind) bool {
	switch k {
	case AuthKindCloud, AuthKindOIDC, AuthKindStatic:
		return true
	}
	return false
}
