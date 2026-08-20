// Package seedmanifest is the pure, no-I/O schema and structural validator
// for §10's ("config/data seeding", §10-P6, §13.4) operator-authored
// seed file. This package never reads a file, never touches the network
// or a database, and never calls time.Now()/crypto/rand (§11) -- it only
// ever converts already-in-memory bytes/structs into a Manifest and
// reports whether that Manifest is internally well-formed. internal/app/
// seed owns reading the file (os.ReadFile), YAML decoding, and everything
// that actually talks to Postgres.
//
// # Why participants carry no role field
//
// §13.4 is explicit: "any imported participants map to users by GitHub
// id; everyone defaults to member, initial admins set by config." The
// Participant struct below has NO Role field at all, by construction --
// there is no key an operator could set on a participant entry that this
// package would ever interpret as a role grant. Initial-admin status is
// resolved entirely from platform.Config.InitialAdminEmails (the SAME
// env-var-driven list internal/adapters/inbound/auth's own
// createUserAndIdentity already uses for a live OAuth first-sign-in,
// explicitly documented there as "the first-run-seeding initial-admin
// list"), by internal/app/seed, never from anything in this file. This is
// deliberate and structural, not an oversight: a seed-file author editing
// this manifest has no field to flip to grant themselves (or anyone else)
// admin -- that authority lives exclusively in deploy-time configuration
// the manifest cannot see or influence.
//
// # Why GitHub identity is a numeric id, never a login
//
// Participant.GitHubID is an int64 -- there is deliberately no
// GitHubLogin field anywhere in this schema. identities.external_id
// (migrations/000003_identities.up.sql) is keyed on the immutable numeric
// GitHub user id, exactly like internal/adapters/inbound/auth's own OAuth
// callback path (strconv.FormatInt(ghUser.ID, 10)) -- a GitHub login can
// be renamed and the freed name re-registered by a different person, so
// resolving identity by login would eventually hand a stranger someone
// else's account and role. Omitting the field entirely (rather than
// accepting one and resolving it) means there is no code path in this
// tool that could ever fall back to a login match. An operator who only
// has logins must resolve them to numeric ids out of band (e.g. `gh api
// users/<login>`) before authoring this file.
package seedmanifest

// Manifest is the full, parsed seed file. Every top-level section is
// optional (a nil/empty slice) -- an operator seeding only secrets, say,
// supplies only that section.
type Manifest struct {
	Participants []Participant `yaml:"participants,omitempty"`
	Secrets      []Secret      `yaml:"secrets,omitempty"`
	Automations  []Automation  `yaml:"automations,omitempty"`
	RepoSettings []RepoSetting `yaml:"repoSettings,omitempty"`
	RWXPreview   []RWXPreview  `yaml:"rwxPreview,omitempty"`
}

// Participant is one imported person, identified ONLY by their immutable
// GitHub numeric id -- see this package's own doc comment for why no
// login field exists and why no role field exists.
type Participant struct {
	// GitHubID is the numeric GitHub user id (GET /user's own "id" field
	// -- see internal/adapters/inbound/auth's own githubUser.ID), NEVER a
	// login/username.
	GitHubID int64 `yaml:"githubId"`
	// Email becomes users.primary_email (NOT NULL UNIQUE,
	// migrations/000002_users.up.sql) -- required; also the ONLY value
	// checked against platform.Config.InitialAdminEmails to decide
	// admin-vs-member (case-insensitive, matching that check's own
	// strings.EqualFold precedent).
	Email string `yaml:"email"`
	// DisplayName becomes users.display_name (NOT NULL).
	DisplayName string `yaml:"displayName"`
}

// SecretScope names the sandbox_secrets scope this tool supports seeding.
// Deliberately a strict subset of sandbox_secret_scope's own 4 DB values
// (migrations/000090_sandbox_secrets.up.sql: automation/environment/repo/
// global) -- see internal/app/seed's own doc.go for why environment and
// automation scope are out of this Step's scope.
type SecretScope string

// The two SecretScope values this tool accepts.
const (
	SecretScopeGlobal SecretScope = "global"
	SecretScopeRepo   SecretScope = "repo"
)

// Secret is one sandbox_secrets row to create (create-if-absent -- see
// internal/app/seed/doc.go for the full idempotency-semantics writeup).
type Secret struct {
	Scope SecretScope `yaml:"scope"`
	// RepoFullName is required (and must be "owner/repo" shaped) when
	// Scope is "repo", and must be empty when Scope is "global".
	RepoFullName string `yaml:"repoFullName,omitempty"`
	// Name is the env-var name this secret is delivered under --
	// validated by internal/domain/sandboxsecret.ValidateName both here
	// (fast, structural feedback) and again by internal/app/seed
	// immediately before every write (defense in depth, same function
	// both times -- never two independent copies of this check).
	Name string `yaml:"name"`
	// Value is the PLAINTEXT secret value. Never logged, never echoed
	// back in any report this tool prints -- see internal/app/seed's own
	// report.go.
	Value string `yaml:"value"`
}

// RepoTarget is one repo an automation fans out against -- the same
// three fields internal/domain/automation.Target and
// restdtos.CreateSessionRequestReposElem already carry.
type RepoTarget struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Branch string `yaml:"branch,omitempty"`
}

// EnvVar is one plain (never-secret) per-automation environment variable
// -- mirrors internal/domain/automation.EnvVar exactly.
type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// AutomationTriggerType is the subset of automation_trigger_type
// (migrations/000055_automations_triggers_and_extras.up.sql) this tool
// supports seeding -- manual/cron/webhook only. github/linear trigger
// types are deliberately out of scope for this Step (see internal/app/
// seed/doc.go): both need a live webhook installed against a real
// repo/workspace, not static manifest data, and the SAME domain
// validators (internal/domain/automation.ValidateTriggerType et al.) this
// package already reuses would need no change to add them later.
type AutomationTriggerType string

// The three AutomationTriggerType values this tool accepts.
const (
	AutomationTriggerManual  AutomationTriggerType = "manual"
	AutomationTriggerCron    AutomationTriggerType = "cron"
	AutomationTriggerWebhook AutomationTriggerType = "webhook"
)

// Automation is one automations row to create (create-if-absent, matched
// by Name -- see internal/app/seed/doc.go: this table has no store-level
// Update for name/prompt/repos/trigger config at all today, so
// create-if-absent is this resource's only sound choice, not merely a
// preference).
type Automation struct {
	Name         string                `yaml:"name"`
	Prompt       string                `yaml:"prompt,omitempty"`
	Repos        []RepoTarget          `yaml:"repos"`
	TriggerType  AutomationTriggerType `yaml:"triggerType"`
	CronSchedule string                `yaml:"cronSchedule,omitempty"` // required iff triggerType is "cron"

	PathScope      []string `yaml:"pathScope,omitempty"`
	MockConfigured bool     `yaml:"mockConfigured,omitempty"`
	ContractsPath  string   `yaml:"contractsPath,omitempty"`

	EnvVars []EnvVar `yaml:"envVars,omitempty"`
}

// RepoSetting is one repo_settings row to upsert (reconcile-to-declared --
// see internal/app/seed/doc.go: repo_settings has never had any write
// path OTHER than upsert anywhere in this codebase, so reconciling here
// matches, rather than invents an exception to, that table's own
// established convention). Each pointer field is independently optional:
// a nil field is left completely untouched (this tool calls the SAME
// column-scoped Upsert* methods httpapi/reposettings.go already uses),
// so a manifest can declare just one flag for a repo without disturbing
// any other toggle a maintainer set through the UI.
type RepoSetting struct {
	RepoFullName           string `yaml:"repoFullName"`
	BlockOnHighRisk        *bool  `yaml:"blockOnHighRisk,omitempty"`
	SentinelAutofixEnabled *bool  `yaml:"sentinelAutofixEnabled,omitempty"`
	AutoMergeEnabled       *bool  `yaml:"autoMergeEnabled,omitempty"`
	AutoRetriggerReview    *bool  `yaml:"autoRetriggerReviewEnabled,omitempty"`
	DescriptionAutofix     *bool  `yaml:"descriptionAutofixEnabled,omitempty"`
	// SessionsEnabled (§10 Phase 6, §32) is the cohort-rollout
	// enrollment gate -- repo_settings.sessions_enabled, migrations/
	// 000096_repo_settings_sessions_enabled.up.sql. Nil (the default) is
	// left completely untouched, exactly like every sibling field on this
	// struct; only meaningful once an operator has set
	// NARVI_ROLLOUT_MODE=cohort (platform.Config.RolloutMode) -- §32's
	// own "seed-manifest-only in v1" design: this is the ONLY writer of
	// this column in this codebase (no REST route exists for it), since
	// REST enrollment is structurally impossible for exactly the repos
	// rollout needs to enroll (see internal/app/seed/reposettings.go's
	// own doc comment for the full "why").
	SessionsEnabled *bool `yaml:"sessionsEnabled,omitempty"`
}

// RWXPreview is one repo's RWX preview integration config (repo_settings.
// rwx_preview_* columns, §4.1.2) -- this tool's concrete answer
// to §10's "integrations" checklist item. See internal/app/seed/
// doc.go for why the OTHER kind of "integration" this codebase names
// (authz.ActionManageIntegrations: Slack/Linear WORKSPACE OAuth
// connections) is deliberately NOT seedable data at all, by any tool --
// it requires a live, human-driven OAuth consent flow with the third
// party, which this file format has no way to fabricate. RWX preview, by
// contrast, is static, operator-known config (a dispatch key + endpoint
// template + org slug), reconciled the same way RepoSetting is.
type RWXPreview struct {
	RepoFullName     string `yaml:"repoFullName"`
	DispatchKey      string `yaml:"dispatchKey"`
	EndpointTemplate string `yaml:"endpointTemplate"`
	OrgSlug          string `yaml:"orgSlug"`
}
