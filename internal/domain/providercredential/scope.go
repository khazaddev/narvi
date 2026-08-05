package providercredential

// Scope is one of the 3 levels a provider_credentials row can be attached
// to -- matches the Postgres provider_credential_scope ENUM
// (migrations/000056_provider_credentials.up.sql) verbatim, one value per
// §25.3's own "sourced per-repo/per-environment/global" line, mirroring
// how the 3 already-reserved RBAC actions (authz.ActionManageRepoSecrets/
// ActionManageEnvSecrets/ActionManageGlobalSecrets) are themselves
// partitioned.
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
)

// scopePriority ranks each Scope from most specific (lowest number) to
// least (highest) -- the single source of truth Resolve's own tie-break
// walks. See this package's own doc.go for the full "why environment
// outranks repo" citation trail (§12.2 item 5 / automation/doc.go, NOT
// the Step 53 brief's own "repo before environment" paraphrase).
var scopePriority = map[Scope]int{
	ScopeEnvironment: 0,
	ScopeRepo:        1,
	ScopeGlobal:      2,
}

// AllScopes is every recognized Scope, in scopePriority's own most-
// specific-first order -- exported so a caller (e.g. a CRUD handler
// validating a request, or a test ranging exhaustively) never needs to
// hand-maintain a second list.
var AllScopes = []Scope{ScopeEnvironment, ScopeRepo, ScopeGlobal}

// IsValidScope reports whether s is one of the 3 recognized Scope values.
func IsValidScope(s Scope) bool {
	_, ok := scopePriority[s]
	return ok
}

// RequiresScopeTarget reports whether s requires a non-empty scope target
// (repo_full_name / environment id) -- true for ScopeRepo/ScopeEnvironment,
// false for ScopeGlobal. Mirrors the CHECK constraint provider_credentials
// _scope_target_id_shape already enforces at the Postgres layer -- this is
// the same rule, available to Go callers (e.g. the CRUD handler) BEFORE a
// request ever reaches a query that would otherwise fail on the DB's own
// constraint.
func RequiresScopeTarget(s Scope) bool {
	return s == ScopeRepo || s == ScopeEnvironment
}
