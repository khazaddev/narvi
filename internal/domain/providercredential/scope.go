package providercredential

// Scope is one of the levels a scoped-secret row can be attached to.
// Originally matched ONLY the Postgres provider_credential_scope ENUM
// (migrations/000056_provider_credentials.up.sql,
// migrations/000061_provider_credentials_user_scope.up.sql) verbatim --
// repo/environment/global (§25.3's own "sourced per-repo/per-environment/
// global" line) plus ScopeUser (Step 59, §29.4). Step 72 (§27.1) widens
// this package's own charter: sandbox_secrets (migrations/
// 000090_sandbox_secrets.up.sql, a SEPARATE table with its own SEPARATE
// Postgres sandbox_secret_scope ENUM) resolves through this SAME Scope
// type and the SAME Resolve function below, adding ScopeAutomation as its
// own most-specific level -- see this package's own doc.go for the full
// "why this package, not a new one" reasoning. The two tables' own
// Postgres ENUMs never share a value set (provider_credential_scope has no
// 'automation'; sandbox_secret_scope has no 'user') and neither table's
// own candidate-building code ever constructs a Candidate of the OTHER
// table's exclusive Scope value -- ScopeUser and ScopeAutomation
// structurally never appear in the same Resolve call, even though both
// are recognized by this one shared vocabulary.
//
// ScopeUser is gated by the own-aware ActionLinkChatGPTAccount (§29.9),
// never managed through the org CRUD endpoints (§29.4: "a user-scope row
// is never managed through the org CRUD endpoints"); ScopeAutomation has
// no CRUD endpoint of ANY kind yet -- §27.1's own "schema-only" carve-out
// -- see AllScopes' own doc comment for what that means for THIS package.
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
	// ScopeAutomation means scope_target_id is an automations.id,
	// stringified -- Step 72's own addition (§27.1), exclusively for
	// sandbox_secrets (provider_credentials has no automation scope and
	// never will: §25.3 scopes that table to repo/environment/global/user
	// only). The MOST specific level for sandbox_secrets' own
	// "automation -> environment -> repo -> global" resolution order
	// (§27.1, doubly confirmed against §12.2 item 5's Settings mockup and
	// automation/doc.go's own deferral note, the SAME two sources this
	// package's doc.go already cites for why environment outranks repo).
	// Schema-only as of Step 72: nothing in this codebase yet WRITES a
	// ScopeAutomation sandbox_secrets row (§8.4/Step 52's own deferred
	// per-automation-secrets follow-up owns that CRUD surface), so no
	// production Resolve call ever actually sees one today -- but the
	// value, and its priority below, are real and load-bearing NOW so
	// that follow-up needs no second migration and no Resolve change,
	// exactly like sandbox_secret_scope's own Postgres ENUM already
	// carries 'automation' from this Step onward.
	ScopeAutomation Scope = "automation"
)

// scopePriority ranks each Scope from most specific (lowest number) to
// least (highest) -- the single source of truth Resolve's own tie-break
// walks. See this package's own doc.go for the full "why environment
// outranks repo" citation trail (§12.2 item 5 / automation/doc.go, NOT
// the Step 53 brief's own "repo before environment" paraphrase). ScopeUser
// and ScopeAutomation both sit at the HEAD, ahead of every org-level
// scope (Step 59, §29.4 for ScopeUser: "a personally-linked account is
// more specific than any environment/repo/global org key"; Step 72, §27.1
// for ScopeAutomation: "automation slots in as the most-specific level"
// for sandbox_secrets' own 4 scopes) -- the two are numbered 0 and 1
// rather than tied, purely to keep this map a genuine total order (a
// future reader should never have to reason about what a tie here would
// mean); their RELATIVE order to each other is arbitrary and never
// actually observed by any production Resolve call, since the two are
// structurally never candidates in the same call (Scope's own doc comment
// above). Every OTHER entry's own relative order is unchanged from
// Step 53.
var scopePriority = map[Scope]int{
	ScopeUser:        0,
	ScopeAutomation:  1,
	ScopeEnvironment: 2,
	ScopeRepo:        3,
	ScopeGlobal:      4,
}

// AllScopes is every recognized Scope, in scopePriority's own most-
// specific-first order -- exported so a caller (e.g. a CRUD handler
// validating a request, or a test ranging exhaustively) never needs to
// hand-maintain a second list. Note that "recognized by this package"
// (IsValidScope true) is a STRICTLY broader set than "reachable through
// any REST DTO's own scope enum" for either consuming table today:
// provider_credentials' own restdtos.ProviderCredentialScope enum omits
// ScopeAutomation (never applicable to that table) AND ScopeUser (managed
// through a completely separate DTO-less flow, §29.4); sandbox_secrets'
// own restdtos.SandboxSecretScope enum likewise omits ScopeUser (never
// applicable) AND ScopeAutomation (§27.1's own "schema-only" carve-out --
// no CRUD endpoint exists for it yet). This package's own Scope type
// deliberately stays the union of everything either table's Resolve call
// might ever see, wider than either wire-level enum.
var AllScopes = []Scope{ScopeUser, ScopeAutomation, ScopeEnvironment, ScopeRepo, ScopeGlobal}

// IsValidScope reports whether s is one of the recognized Scope values.
func IsValidScope(s Scope) bool {
	_, ok := scopePriority[s]
	return ok
}

// RequiresScopeTarget reports whether s requires a non-empty scope target
// (repo_full_name / environment id / user id / automation id) -- true for
// ScopeRepo/ScopeEnvironment/ScopeUser/ScopeAutomation, false for
// ScopeGlobal. Mirrors the CHECK constraint every scoped-secret table's
// own "_scope_target_id_shape" constraint already enforces at the
// Postgres layer (provider_credentials_scope_target_id_shape,
// sandbox_secrets_scope_target_id_shape) -- this is the same rule,
// available to Go callers (e.g. a CRUD handler) BEFORE a request ever
// reaches a query that would otherwise fail on the DB's own constraint.
func RequiresScopeTarget(s Scope) bool {
	return s == ScopeRepo || s == ScopeEnvironment || s == ScopeUser || s == ScopeAutomation
}
