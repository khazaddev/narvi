package providercredential

// Scope is one of the levels a provider_credentials row can be attached
// to -- matches the Postgres provider_credential_scope ENUM
// (migrations/000056_provider_credentials.up.sql,
// migrations/000061_provider_credentials_user_scope.up.sql) verbatim, one
// value per §25.3's own "sourced per-repo/per-environment/global" line
// (repo/environment/global) plus ScopeUser (Step 59, §29.4), mirroring how
// the 3 already-reserved RBAC actions (authz.ActionManageRepoSecrets/
// ActionManageEnvSecrets/ActionManageGlobalSecrets) are themselves
// partitioned for the org-scoped 3 -- ScopeUser has no such RBAC-action
// analog: it is gated by the own-aware ActionLinkChatGPTAccount instead
// (§29.9), never managed through the org CRUD endpoints (§29.4: "a
// user-scope row is never managed through the org CRUD endpoints").
type Scope string

const (
	// ScopeRepo means scope_target_id is a repo_full_name ("owner/repo",
	// the same natural key repo_settings.repo_full_name already uses).
	ScopeRepo Scope = "repo"
	// ScopeEnvironment means scope_target_id is an environments.id,
	// stringified.
	ScopeEnvironment Scope = "environment"
	// ScopeGlobal means scope_target_id is always NULL/absent -- this
	// credential applies org-wide, with nothing more specific configured.
	ScopeGlobal Scope = "global"
	// ScopeUser means scope_target_id is a users.id, stringified -- a
	// personally-linked account (Step 59, §29.4: a ChatGPT Plus/Pro
	// subscription is an individual seat), more specific than any
	// environment/repo/global org key. v1 creates ScopeUser rows ONLY via
	// the ChatGPT link flow (internal/app/chatgptlink), always kind=oauth
	// -- there is deliberately no generic creating endpoint for a
	// user-scoped row of any other kind (§29.4).
	ScopeUser Scope = "user"
)

// scopePriority ranks each Scope from most specific (lowest number) to
// least (highest) -- the single source of truth Resolve's own tie-break
// walks. See this package's own doc.go for the full "why environment
// outranks repo" citation trail (§12.2 item 5 / automation/doc.go, NOT
// the Step 53 brief's own "repo before environment" paraphrase). ScopeUser
// sits at the HEAD (Step 59, §29.4: "a personally-linked account is more
// specific than any environment/repo/global org key") -- every other
// entry's own relative order is unchanged from Step 53.
var scopePriority = map[Scope]int{
	ScopeUser:        0,
	ScopeEnvironment: 1,
	ScopeRepo:        2,
	ScopeGlobal:      3,
}

// AllScopes is every recognized Scope, in scopePriority's own most-
// specific-first order -- exported so a caller (e.g. a CRUD handler
// validating a request, or a test ranging exhaustively) never needs to
// hand-maintain a second list.
var AllScopes = []Scope{ScopeUser, ScopeEnvironment, ScopeRepo, ScopeGlobal}

// IsValidScope reports whether s is one of the recognized Scope values.
func IsValidScope(s Scope) bool {
	_, ok := scopePriority[s]
	return ok
}

// RequiresScopeTarget reports whether s requires a non-empty scope target
// (repo_full_name / environment id / user id) -- true for ScopeRepo/
// ScopeEnvironment/ScopeUser, false for ScopeGlobal. Mirrors the CHECK
// constraint provider_credentials_scope_target_id_shape already enforces
// at the Postgres layer -- this is the same rule, available to Go callers
// (e.g. the CRUD handler) BEFORE a request ever reaches a query that would
// otherwise fail on the DB's own
// constraint.
func RequiresScopeTarget(s Scope) bool {
	return s == ScopeRepo || s == ScopeEnvironment || s == ScopeUser
}
