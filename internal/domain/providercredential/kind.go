package providercredential

// Kind distinguishes a static API-key credential from an OAuth-derived
// one -- matches the Postgres provider_credential_kind ENUM
// (migrations/000062_chatgpt_oauth_credentials.up.sql) verbatim (
// §29.4). Every row created before this Step is, and remains, KindAPIKey
// (the column's own DB DEFAULT) -- Kind is a new axis alongside Scope and
// Provider, not a replacement for either: a KindOAuth row is always
// ScopeUser today (§29.4's "v1 creates user-scope rows ONLY via the link
// flow... kind='oauth'"), but the two are independent columns, not a
// single combined enum, so a future static per-user API key
// (structurally representable, deliberately unbuilt, §29.4) would be
// ScopeUser+KindAPIKey without needing a new Scope or Kind value.
type Kind string

const (
	// KindAPIKey is a plaintext-at-encryption static credential (an API
	// key), delivered to a sandbox as an env var (providercredentialsdelivery
	// .go's "api" Auth-union member, §29.6). This is every provider_
	// credentials row's Kind before and the column's own DB
	// DEFAULT.
	KindAPIKey Kind = "api_key"
	// KindOAuth is a token pair obtained via an OAuth device/browser flow
	// (§29.2), delivered to a sandbox via OpenCode's own auth-store API
	// (the "oauth" Auth-union member, §29.6) rather than an env var --
	// its refresh token never leaves the control plane (§29.5).
	KindOAuth Kind = "oauth"
)

// AllKinds is every recognized Kind, in this file's own declaration
// order -- exported so a caller (e.g. a test ranging exhaustively) never
// needs to hand-maintain a second list.
var AllKinds = []Kind{KindAPIKey, KindOAuth}

// IsValidKind reports whether k is one of the recognized Kind values.
func IsValidKind(k Kind) bool {
	switch k {
	case KindAPIKey, KindOAuth:
		return true
	}
	return false
}
