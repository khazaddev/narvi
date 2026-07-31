// This file (config.go) implements typed config validated at boot,
// fail-fast, with named errors (§5.4, §11).

package platform

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/reposource"
)

// Stage identifies which deployment stage the control plane is running in.
// Typed (rather than a bare string) so an invalid value fails fast at boot
// instead of silently propagating.
//
// Named Stage, not Environment: the technical plan already reserves
// "Environment" for a distinct, load-bearing domain entity (repo/automation
// config with path scoping and secrets — §14.1, §12.2 item 5, PR_PLAN row
// 10 "domain: Environment scoping"). Reusing that name here for an unrelated
// deploy-stage concept would collide with it once PR-10 lands.
type Stage string

// The only valid Stage values. Load rejects anything else.
const (
	StageDevelopment Stage = "development"
	StageStaging     Stage = "staging"
	StageProduction  Stage = "production"
)

// envVarName is the process environment variable Load reads to select
// Stage. Required in every stage -- no default (see Load: an unset value
// appends a *MissingRequiredEnvError, it is never silently treated as
// StageDevelopment). A production deploy that simply forgot to set this
// must fail to boot, not boot as if it were development -- WithAuthSessionCookie
// (authcookie.go) derives the Secure cookie attribute directly from
// cfg.Stage != StageDevelopment, so a silent development default would
// silently omit Secure on every auth cookie a misconfigured production
// deploy mints.
const envVarName = "NARVI_STAGE"

// InvalidStageError is returned by Load when NARVI_STAGE is set to a value
// that is not one of StageDevelopment, StageStaging, or StageProduction.
type InvalidStageError struct {
	Value string
}

func (e *InvalidStageError) Error() string {
	return fmt.Sprintf("invalid %s=%q: must be one of %q, %q, %q",
		envVarName, e.Value, StageDevelopment, StageStaging, StageProduction)
}

// logLevelEnvVarName is the process environment variable Load reads to
// select LogLevel (PR-03, §5.3).
const logLevelEnvVarName = "NARVI_LOG_LEVEL"

// defaultLogLevelValue is the NARVI_LOG_LEVEL value Load assumes when the
// variable is unset. Unlike NARVI_STAGE (required, no default -- see
// envVarName above), a missing log level has a genuinely safe fallback,
// so this one still defaults quietly.
const defaultLogLevelValue = "info"

// InvalidLogLevelError is returned by Load when NARVI_LOG_LEVEL is set to a
// value that is not (case-insensitively) one of debug, info, warn, or
// error. Named and structured the same way as InvalidStageError.
type InvalidLogLevelError struct {
	Value string
}

func (e *InvalidLogLevelError) Error() string {
	return fmt.Sprintf("invalid %s=%q: must be one of %q, %q, %q, %q (case-insensitive)",
		logLevelEnvVarName, e.Value, "debug", "info", "warn", "error")
}

// parseLogLevel validates raw (already defaulted to defaultLogLevelValue if
// the env var was unset) against the four accepted spellings, case-
// insensitively, and returns the corresponding slog.Level. Deliberately an
// explicit switch (not slog.Level.UnmarshalText) so only exactly these four
// names are accepted — UnmarshalText also accepts numeric offset suffixes
// like "error-8", which is more than this env var is specified to take.
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, &InvalidLogLevelError{Value: raw}
	}
}

// databaseURLEnvVarName is the process environment variable Load reads for
// the Postgres DSN (PR-06, §1's stack-choices line: golang-migrate +
// pgx/v5). Required — no default, since there is no safe placeholder DSN.
const databaseURLEnvVarName = "NARVI_DATABASE_URL"

// httpAddrEnvVarName is the process environment variable Load reads for the
// control-plane HTTP listen address (PR-06). Optional: an unset/empty value
// defaults to defaultHTTPAddr.
const httpAddrEnvVarName = "NARVI_HTTP_ADDR"

// defaultHTTPAddr is the NARVI_HTTP_ADDR value Load assumes when the
// variable is unset -- a genuinely safe fallback, unlike NARVI_STAGE
// (required, no default -- see envVarName above).
const defaultHTTPAddr = ":8080"

// hmacSandboxSecretEnvVarName, hmacBotsSecretEnvVarName, and
// hmacWebhookSecretEnvVarName are the three per-direction HMAC secret
// env vars §5.2 requires: "Separate secrets per direction (sandbox→CP,
// CP→bots, webhook ingress) so one rotation doesn't touch everything."
// All three are required in every stage, including development — Load
// never supplies a baked-in default value for any of them.
const (
	hmacSandboxSecretEnvVarName = "NARVI_HMAC_SANDBOX_SECRET"
	hmacBotsSecretEnvVarName    = "NARVI_HMAC_BOTS_SECRET"
	hmacWebhookSecretEnvVarName = "NARVI_HMAC_WEBHOOK_SECRET"
)

// MissingRequiredEnvError is returned by Load when a required environment
// variable that has no safe default (NARVI_DATABASE_URL today) is unset or
// empty.
type MissingRequiredEnvError struct {
	EnvVar string
}

func (e *MissingRequiredEnvError) Error() string {
	return fmt.Sprintf("missing required %s (no default)", e.EnvVar)
}

// InvalidHMACSecretError is returned by Load when one of the three
// direction-specific HMAC secret env vars (§5.2) is unset or empty. A
// distinct, named error (same pattern as InvalidStageError/
// InvalidLogLevelError above) so a misconfigured deploy names exactly
// which direction's secret is missing, never a single generic "config
// invalid" — and so that, per §5.2, no direction is ever silently
// defaulted.
type InvalidHMACSecretError struct {
	EnvVar string
}

func (e *InvalidHMACSecretError) Error() string {
	return fmt.Sprintf(
		"missing required %s: every stage (including development) must set its own HMAC secret — §5.2 requires separate secrets per direction, never a baked-in default",
		e.EnvVar,
	)
}

// gitHubClientIDEnvVarName, gitHubClientSecretEnvVarName, and
// publicBaseURLEnvVarName are the env vars Load reads for Step 20's
// ("auth v1", §13.1) GitHub OAuth wiring. All three are required in every
// stage — never defaulted, matching the 3 HMAC secrets' own "never a
// baked-in default" convention.
const (
	gitHubClientIDEnvVarName     = "NARVI_GITHUB_CLIENT_ID"
	gitHubClientSecretEnvVarName = "NARVI_GITHUB_CLIENT_SECRET"
	publicBaseURLEnvVarName      = "NARVI_PUBLIC_BASE_URL"
)

// gitHubWebhookSecretEnvVarName and gitHubBotHandleEnvVarName configure
// Step 32's ("GitHub ingress", §8.2) own webhook adapter --
// internal/adapters/inbound/github. gitHubWebhookSecretEnvVarName is
// DELIBERATELY DISTINCT from hmacWebhookSecretEnvVarName above: that
// secret backs Narvi's OWN internal bearer HMAC scheme (a later, unrelated
// concern -- see internal/platform/webhooksig.go's own doc comment for
// the full reasoning), never a real third-party provider signature.
// GitHubWebhookSecret is the REAL secret GitHub itself signs
// "X-Hub-Signature-256" with, configured on GitHub's own webhook settings
// screen. gitHubBotHandleEnvVarName is the bot/app username Step 32's own
// mention-detection matches comment bodies against (a plain "@handle"
// substring check, internal/adapters/inbound/github). Both required in
// every stage -- never defaulted, matching every other secret/credential
// this file already reads (the 3 HMAC secrets, the GitHub OAuth
// credentials, TokenEncryptionKey, Modal's own BaseURL/AuthToken): there
// is no safe placeholder webhook secret, and a misconfigured/empty bot
// handle would silently make this entire ingress route never detect a
// single mention.
const (
	gitHubWebhookSecretEnvVarName = "NARVI_GITHUB_WEBHOOK_SECRET"
	gitHubBotHandleEnvVarName     = "NARVI_GITHUB_BOT_HANDLE"
)

// gitHubBotTokenEnvVarName configures Step 35's ("outbox delivery", §5.1)
// own GitHub Notifier adapter (internal/adapters/outbound/githubapi's new
// issue-comment-posting method) -- read from NARVI_GITHUB_BOT_TOKEN.
// Required in every stage -- never defaulted, matching every other secret
// this file already reads. Deliberately a SEPARATE credential from every
// existing GitHub-flavored value in this struct: GitHubClientID/
// GitHubClientSecret authenticate the OAuth APP a human signs into Narvi
// through (§13.1); ports.SourceControl.CreatePR (githubapi.Adapter,
// already-existing) authenticates PER-CALL with the SESSION CREATOR's own
// decrypted OAuth access token (internal/adapters/outbound/postgres.
// IdentityStore) -- but a webhook-originated (GitHub/Slack/Linear) session
// has sessions.created_by left NULL (migrations/000004_sessions.up.sql),
// so there is no logged-in Narvi user's own token to reuse for posting an
// async turn-outcome comment back to a PR. GitHubBotToken is instead a
// single, statically-configured bot/app credential (a real GitHub personal
// access token or a GitHub App installation token, whichever the deploying
// operator provisions), baked into the GitHub Notifier adapter once at
// construction time (cmd/control-plane/main.go), never looked up per
// session the way CreatePR's own spec.Token is.
//
// Batch fix/audit-github-pr-payload-correctness (H5 audit fix) adds a
// SECOND consumer of this SAME credential: internal/adapters/inbound/
// github's own webhook handler authenticates its own GetPullRequest call
// (resolving an issue_comment mention's TRUE head branch/repo) with it too
// -- the same "no logged-in Narvi user's own token available" reasoning
// applies identically there (a GitHub webhook mention carries no
// per-commenter OAuth token either).
const gitHubBotTokenEnvVarName = "NARVI_GITHUB_BOT_TOKEN"

// gitHubImageBuildTokenEnvVarName configures Step 42's ("warm boot:
// refresh pump + hook policy", §19.2) own platform-level GitHub
// credential, read from NARVI_GITHUB_IMAGE_BUILD_TOKEN. §19.2's own
// words: "the freshness pump needs GitHub credentials belonging to no
// session creator... a shared image has no creator... the recommendation
// is a platform-level credential (GitHub App installation token) shared
// by the freshness pump and the build service."
//
// DELIBERATELY OPTIONAL -- unlike every other GitHub-flavored secret this
// file reads (GitHubClientID/Secret, GitHubWebhookSecret, GitHubBotToken,
// all required, never defaulted): this credential's own consumers (the
// freshness pump's per-repo tip-SHA resolution, app/imagebuild.Builder's
// claim-time SHA resolution for a repo-bearing row) are explicitly
// designed to degrade cleanly on its absence rather than treat a missing
// value as a boot-time configuration error: a missing/invalid credential
// or a GitHub API failure resolving a tip SHA is logged and recorded as a
// failed attempt via the SAME retry/backoff path any other resolution
// failure uses (imagebuild.Builder's resolveRepoSHAs/attempt) -- never a
// crash, and never something Load itself rejects at startup. A deploy
// that has not yet provisioned this credential simply never
// refreshes/builds a repo-bearing shared image -- every spawn still
// falls back to the base image exactly as it always has (§10 Phase 2's
// own "always fall back to base image on any miss" invariant,
// unaffected).
//
// How this differs from GitHubBotToken (see that field's own doc comment
// for its full reasoning): GitHubBotToken is a REQUIRED, already-used,
// operator-provisioned-once credential backing a real GitHub identity
// (posting PR comments as a bot, resolving a webhook mention's true PR)
// -- its own doc comment describes it as "a real GitHub personal access
// token or a GitHub App installation token, whichever the deploying
// operator provisions," never auto-refreshed, with no App-ID/private-key/
// installation-token-refresh plumbing anywhere in this codebase.
// GitHubImageBuildToken is a DIFFERENT, NEW, distinct credential for a
// completely different purpose (read-only tip-SHA resolution for a
// session-creator-independent background pump, not comment-posting or
// PR-lookup identity) -- reusing GitHubBotToken here would conflate two
// unrelated rotation/scoping boundaries the whole point of "separate
// secrets per direction" (§5.2) argues against, and would make this
// pump's own failure mode (a missing/invalid credential) impossible to
// diagnose independently of GitHubBotToken's own, unrelated consumers.
// Plain string, same shape as every other token this file reads (a real
// GitHub personal access token or a GitHub App installation token,
// whichever the deploying operator provisions) -- never logged anywhere.
const gitHubImageBuildTokenEnvVarName = "NARVI_GITHUB_IMAGE_BUILD_TOKEN"

// tokenEncryptionKeyEnvVarName is the env var Load reads for the AES-256-GCM
// key protecting provider tokens at rest (§13.1: "Provider tokens encrypted
// at rest (AES-GCM), per-user"). Required in every stage; the raw value
// must base64-decode to exactly tokenEncryptionKeyByteLength bytes.
const tokenEncryptionKeyEnvVarName = "NARVI_TOKEN_ENCRYPTION_KEY"

// tokenEncryptionKeyByteLength is the exact decoded key length AES-256-GCM
// requires. A plain byte count, not a duration/interval, so (matching
// tokenhash.go's own wsTokenByteLength precedent) it is an ordinary Go
// constant rather than a platform.Timeouts field.
const tokenEncryptionKeyByteLength = 32

// InvalidTokenEncryptionKeyError is returned by Load when
// NARVI_TOKEN_ENCRYPTION_KEY fails either of its two checks: the raw value
// isn't valid base64, or it decodes to something other than exactly
// tokenEncryptionKeyByteLength bytes. Reason names which check failed —
// never a bare "invalid key".
type InvalidTokenEncryptionKeyError struct {
	Reason string
}

func (e *InvalidTokenEncryptionKeyError) Error() string {
	return fmt.Sprintf("invalid %s: %s", tokenEncryptionKeyEnvVarName, e.Reason)
}

// allowedEmailDomainsEnvVarName, allowedGitHubOrgsEnvVarName, and
// allowedEmailsEnvVarName are the 3 signup-allowlist mechanisms §13.1
// names ("allowlist of email domains / GitHub orgs / explicit users").
// Each is individually optional (a comma-separated list, parsed by
// parseCommaSeparatedList), but Load fails fast if ALL THREE are empty —
// see EmptyAllowlistError.
const (
	allowedEmailDomainsEnvVarName = "NARVI_ALLOWED_EMAIL_DOMAINS"
	allowedGitHubOrgsEnvVarName   = "NARVI_ALLOWED_GITHUB_ORGS"
	allowedEmailsEnvVarName       = "NARVI_ALLOWED_EMAILS"
)

// EmptyAllowlistError is returned by Load when NARVI_ALLOWED_EMAIL_DOMAINS,
// NARVI_ALLOWED_GITHUB_ORGS, and NARVI_ALLOWED_EMAILS are ALL empty. An
// allowlist that allows nobody by omission is a footgun this codebase's
// own "never a baked-in permissive default" convention (the 3 HMAC
// secrets already never have a default) argues against — the operator
// must configure at least one allowlist mechanism explicitly, in every
// stage including development.
type EmptyAllowlistError struct{}

func (e *EmptyAllowlistError) Error() string {
	return fmt.Sprintf(
		"at least one of %s, %s, %s must be set (§13.1's signup allowlist) — an allowlist that allows nobody by omission is not a safe default",
		allowedEmailDomainsEnvVarName, allowedGitHubOrgsEnvVarName, allowedEmailsEnvVarName,
	)
}

// modalBaseURLEnvVarName, modalAuthTokenEnvVarName, and
// modalEgressProxyURLEnvVarName configure the real
// internal/adapters/outbound/modal.Provider construction in cmd/
// control-plane/main.go (Step 21, "e2e happy path" -- this Step is the
// SandboxProvider's first real production caller). BaseURL/AuthToken are
// required in every stage, matching every other "never a baked-in
// default" secret this file already reads (the 3 HMAC secrets, the GitHub
// OAuth credentials) -- there genuinely is no safe placeholder Modal
// endpoint/token. EgressProxyURL is optional (§4.1: "the configurable
// egress proxy" is itself optional/fail-open at the modal package's own
// New constructor).
const (
	modalBaseURLEnvVarName        = "NARVI_MODAL_BASE_URL"
	modalAuthTokenEnvVarName      = "NARVI_MODAL_AUTH_TOKEN"
	modalEgressProxyURLEnvVarName = "NARVI_MODAL_EGRESS_PROXY_URL"
)

// openCodeRuntimeVersionEnvVarName is the env var Load reads for Step 26's
// ("image builds", §8.5-note/§10-P2) own RuntimeVersion fingerprint input
// (domain/imagebuild.Fingerprint's third argument) -- the pinned OpenCode
// version this control plane assumes every base/prebuilt sandbox image
// carries. Optional: defaults to defaultOpenCodeRuntimeVersion.
//
// Residual drift risk, named honestly rather than silently accepted: this
// default is a SEPARATE literal from .github/workflows/ci.yml's own
// `opencode-ai@1.17.15` pin (confirmed by a repo-wide grep before this Step
// started: nothing today centralizes the OpenCode version pin as a single
// Go-level constant/config value that CI and this default could both read
// from) -- nothing mechanically keeps the two in sync, so a future version
// bump that updates ci.yml without ALSO updating either this default or a
// deploy's own NARVI_OPENCODE_RUNTIME_VERSION override would silently
// fingerprint every image against a stale runtime version. Wiring CI to
// read this same value (or vice versa) is a natural follow-up, not built
// here (this Step's own scope is the fingerprint mechanism, not a build-
// tooling refactor of an unrelated, already-merged CI workflow).
const openCodeRuntimeVersionEnvVarName = "NARVI_OPENCODE_RUNTIME_VERSION"

// defaultOpenCodeRuntimeVersion is the NARVI_OPENCODE_RUNTIME_VERSION value
// Load assumes when the variable is unset -- kept equal to ci.yml's own
// current pin at the time this Step was written; see
// openCodeRuntimeVersionEnvVarName's own doc comment for the drift risk
// this equality is not mechanically guaranteed to survive.
const defaultOpenCodeRuntimeVersion = "1.17.15"

// linearWebhookSecretEnvVarName, linearOAuthClientIDEnvVarName, and
// linearOAuthClientSecretEnvVarName are the env vars Load reads for Step
// 34's ("Linear ingress", §8.10) Linear wiring. All three are required in
// every stage — never defaulted, matching every other "never a baked-in
// default" secret this file already reads.
//
// linearWebhookSecretEnvVarName is deliberately a SEPARATE secret from
// hmacWebhookSecretEnvVarName above: that one backs platform.Sign/Verify's
// own internal "{timestamp}.{signature}" bearer format (see
// internal/platform/webhooksig.go's own doc comment), never a real
// provider's webhook signature. This one is Linear's own real webhook
// signing secret ("You can find the signing secret on the webhook's
// detail page" — confirmed against Linear's real, current developer docs
// during this Step's investigation), verified via
// platform.VerifyWebhookSignature against Linear's own real scheme: a
// hex-encoded HMAC-SHA256 of the raw request body, presented in the
// Linear-Signature header — no "sha256="-style prefix, unlike GitHub's.
//
// linearOAuthClientIDEnvVarName/linearOAuthClientSecretEnvVarName are a
// SEPARATE OAuth application from GitHubClientID/GitHubClientSecret above:
// that one is a human signing into Narvi's own web UI (§13.1); this one
// authorizes a Linear WORKSPACE installation (Linear's own OAuth2 flow,
// with the `actor=app` authorization-url parameter that "switches to an
// app installation" at workspace scope) so the control plane can call
// Linear's own API on that workspace's behalf — never a second way for a
// human to log into Narvi itself.
const (
	linearWebhookSecretEnvVarName     = "NARVI_LINEAR_WEBHOOK_SECRET"
	linearOAuthClientIDEnvVarName     = "NARVI_LINEAR_CLIENT_ID"
	linearOAuthClientSecretEnvVarName = "NARVI_LINEAR_CLIENT_SECRET"
)

// linearDefaultRepoNameEnvVarName and linearDefaultRepoURLEnvVarName name
// the single repo a Linear-originated session's Repos field is populated
// with (both required, no default). Scope note (Step 34): Linear's own
// AgentSessionEvent webhook payload carries no repository information at
// all (confirmed against Linear's real schema during this Step's
// investigation — an agent is expected to either already know its own
// candidate repos, via the SEPARATE issueRepositorySuggestions API, or be
// told out of band), and every CreateSessionRequest requires a non-empty
// Repos list (contracts/rest/v1/dtos.schema.json) regardless of ingress
// surface. §14/§8.4's own per-workspace/per-team repo mapping (Automations
// config) does not exist yet — building it is out of this Step's scope.
// Until that lands, every Linear-originated session targets this ONE
// operator-configured repo; a future Step naturally replaces this with a
// real per-team/per-workspace mapping once Automations config exists.
const (
	linearDefaultRepoNameEnvVarName = "NARVI_LINEAR_DEFAULT_REPO_NAME"
	linearDefaultRepoURLEnvVarName  = "NARVI_LINEAR_DEFAULT_REPO_URL"
)

// slackSigningSecretEnvVarName and slackBotTokenEnvVarName configure Step
// 33's ("Slack ingress", §8.10) real Slack Events API adapter
// (internal/adapters/inbound/slack). Deliberately NOT HMACWebhookSecret --
// see internal/platform/webhooksig.go's own doc comment for why a real
// provider's own signature scheme never matches that internal bearer
// format. SlackSigningSecret verifies "X-Slack-Signature"/
// "X-Slack-Request-Timestamp" on every inbound webhook request (fail
// closed); SlackBotToken authenticates the one direct
// chat.postMessage call this Step's own in-thread ack makes (see that
// package's own doc.go for why this is a single direct API call, not the
// general Notifier/outbox abstraction Step 35 builds). Both required in
// every stage -- never defaulted, matching every other secret this file
// already reads.
const (
	slackSigningSecretEnvVarName = "NARVI_SLACK_SIGNING_SECRET"
	slackBotTokenEnvVarName      = "NARVI_SLACK_BOT_TOKEN"
)

// slackDefaultRepoNameEnvVarName and slackDefaultRepoURLEnvVarName name the
// single repo a brand-new Slack-spawned session's CreateSessionRequest
// carries (Step 33, "Slack ingress"). Deliberately OPTIONAL, unlike the two
// vars above: the technical plan has no per-channel/per-workspace repo
// routing design yet (that is a genuinely open gap, left to a future
// automations/routing Step), so this Step's own honest, minimal stand-in is
// exactly one operator-configured default repo. Leaving either one unset
// disables NEW-thread session creation only (see internal/adapters/inbound/
// slack's own doc.go) -- it does not affect replies on an already-mapped
// thread, which never need a repo at all.
const (
	slackDefaultRepoNameEnvVarName = "NARVI_SLACK_DEFAULT_REPO_NAME"
	slackDefaultRepoURLEnvVarName  = "NARVI_SLACK_DEFAULT_REPO_URL"
)

// anthropicAPIKeyEnvVarName and intentClassifierModelEnvVarName configure
// Step 36's ("intent classifier", §8.3/§18) real Anthropic adapter
// (internal/adapters/outbound/llm), read from NARVI_ANTHROPIC_API_KEY /
// NARVI_INTENT_CLASSIFIER_MODEL. Both required in every stage -- never
// defaulted, matching every other secret/required-choice this file
// already reads. intentClassifierProviderEnvVarName names the SEPARATE,
// explicitly-validated provider dimension (§8's own "multi-provider by
// nature" requirement: Provider is never inferred from the model string's
// own naming convention) -- also required, no default, so a deploy that
// forgets to set it fails fast at boot rather than silently resolving to
// whichever provider happens to be registered first.
const (
	anthropicAPIKeyEnvVarName          = "NARVI_ANTHROPIC_API_KEY"
	intentClassifierProviderEnvVarName = "NARVI_INTENT_CLASSIFIER_PROVIDER"
	intentClassifierModelEnvVarName    = "NARVI_INTENT_CLASSIFIER_MODEL"
)

// intentClassifierActiveSurfacesEnvVarName is the env var Load reads for
// §18.5's permanent shadow-vs-active gate (see
// Config.IntentClassifierActiveSurfaces's own doc comment). Optional --
// an empty/unset value means every surface defaults to shadow mode.
const intentClassifierActiveSurfacesEnvVarName = "NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES"

// Model choice (the Makefile dev target's own example value:
// "claude-haiku-4-5", Claude Haiku 4.5 -- NOT applied as a silent Load()
// default; Provider/Model are both required, no default, per the const
// block above): a classification call run on EVERY session across EVERY
// ingress surface is a high-volume, cost- and latency-sensitive internal
// call, quite unlike a user-facing generative task (§18.1: "a fast, low-
// complexity, high-volume, latency-sensitive call, not a 'remotely
// complicated' reasoning task"). Haiku 4.5 is Anthropic's fastest/
// cheapest current-generation model with structured-output support --
// the right tier for a call this codebase's own SessionStore will end up
// making once per session, forever, rather than the biggest available
// model. An unrecognized/misconfigured NARVI_INTENT_CLASSIFIER_MODEL
// value maps to FallbackReasonUnsupportedProvider at classification time
// (never a silent substitution) -- see internal/adapters/outbound/llm's
// own model-recognition table.

// initialAdminEmailsEnvVarName is the env var Load reads for the
// first-run-seeding initial-admin list (§13.4: "initial admins set by
// config"). Optional — an empty list simply means every first-time
// sign-in defaults to role "member".
const initialAdminEmailsEnvVarName = "NARVI_INITIAL_ADMIN_EMAILS"

// parseCommaSeparatedList splits raw on commas, trims whitespace from each
// entry, and drops empty entries — used for every optional
// comma-separated-list env var this file reads (the 3 allowlist mechanisms
// plus InitialAdminEmails). An empty/unset raw value returns a nil slice,
// never a slice containing one empty string.
func parseCommaSeparatedList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// Config is the top-level, typed control-plane configuration, validated
// once at boot (§5.4, §11: "typed config validated at boot, fail-fast,
// named errors").
type Config struct {
	Stage    Stage
	Timeouts Timeouts

	// LogLevel is the minimum slog level the process logs at (PR-03, §5.3),
	// read from NARVI_LOG_LEVEL (debug/info/warn/error, case-insensitive,
	// default "info").
	LogLevel slog.Level

	// DatabaseURL is the Postgres DSN used by
	// adapters/outbound/postgres.NewPool and the boot-time migration run
	// (PR-06), read from NARVI_DATABASE_URL. Required; Load does not
	// validate that the DSN itself parses beyond non-empty — pgxpool.New
	// surfaces a real connection error at boot if it's malformed.
	DatabaseURL string

	// HTTPAddr is the address `narvi serve` listens on (PR-06), read from
	// NARVI_HTTP_ADDR. Optional: defaults to ":8080".
	HTTPAddr string

	// HMACSandboxSecret, HMACBotsSecret, and HMACWebhookSecret are the
	// three direction-specific secrets §5.2 requires ("Separate secrets
	// per direction (sandbox→CP, CP→bots, webhook ingress) so one
	// rotation doesn't touch everything"), read from
	// NARVI_HMAC_SANDBOX_SECRET, NARVI_HMAC_BOTS_SECRET, and
	// NARVI_HMAC_WEBHOOK_SECRET respectively. All three are required in
	// every stage, including development — never defaulted.
	//
	// HMACWebhookSecret note (Step 31, "webhook toolkit"): this secret
	// pairs with platform.Sign/Verify's own internal "{timestamp}.
	// {signature}" bearer format (hmacauth.go) -- it is NOT the secret
	// GitHub/Slack/Linear ingress adapters (Steps 32-34) use to verify
	// their OWN provider's webhook signature (a real provider signature
	// never matches this bearer format at all; see
	// internal/platform/webhooksig.go's own doc comment for the full
	// reasoning and each provider's own scheme). Each of those adapters
	// reads its own, separate, provider-specific secret instead.
	HMACSandboxSecret string
	HMACBotsSecret    string
	HMACWebhookSecret string

	// GitHubClientID and GitHubClientSecret are the GitHub OAuth App
	// credentials (§13.1: "GitHub OAuth is the primary login"), read from
	// NARVI_GITHUB_CLIENT_ID / NARVI_GITHUB_CLIENT_SECRET. Both required in
	// every stage — never defaulted. GitHubClientSecret is never logged
	// anywhere (see internal/adapters/inbound/auth's own security notes).
	GitHubClientID     string
	GitHubClientSecret string

	// GitHubWebhookSecret and GitHubBotHandle configure Step 32's
	// ("GitHub ingress", §8.2) webhook adapter, read from
	// NARVI_GITHUB_WEBHOOK_SECRET / NARVI_GITHUB_BOT_HANDLE. Both required
	// in every stage -- never defaulted. See gitHubWebhookSecretEnvVarName's
	// own doc comment above for why GitHubWebhookSecret is a DISTINCT
	// secret from HMACWebhookSecret.
	GitHubWebhookSecret string
	GitHubBotHandle     string

	// GitHubBotToken configures Step 35's ("outbox delivery", §5.1) own
	// GitHub Notifier adapter, read from NARVI_GITHUB_BOT_TOKEN. Required
	// in every stage -- never defaulted. See gitHubBotTokenEnvVarName's own
	// doc comment above for why this is a distinct credential from every
	// other GitHub-flavored value in this struct -- and, since batch
	// fix/audit-github-pr-payload-correctness (H5 audit fix), also for
	// GitHub ingress's own GetPullRequest call (cmd/control-plane/main.go's
	// githubingress.Config.BotToken). Never logged.
	GitHubBotToken string

	// GitHubImageBuildToken is Step 42's ("warm boot: refresh pump + hook
	// policy", §19.2) own platform-level GitHub credential, read from
	// NARVI_GITHUB_IMAGE_BUILD_TOKEN. Empty string means "not configured" --
	// see gitHubImageBuildTokenEnvVarName's own doc comment for why this,
	// uniquely among this struct's GitHub-flavored fields, is deliberately
	// OPTIONAL and how it differs from GitHubBotToken. Never logged.
	GitHubImageBuildToken string

	// PublicBaseURL is this control plane's own externally-reachable base
	// URL (e.g. "http://localhost:8080" in development, a real https://
	// URL in production), read from NARVI_PUBLIC_BASE_URL. Required — used
	// to construct the OAuth RedirectURL as PublicBaseURL +
	// "/auth/github/callback" (internal/adapters/inbound/auth.
	// NewGitHubOAuthConfig). Not validated as a well-formed URL beyond
	// non-empty, matching DatabaseURL's own precedent above.
	PublicBaseURL string

	// TokenEncryptionKey is the already-decoded, exactly-32-byte AES-256-GCM
	// key protecting provider tokens at rest (§13.1), read from
	// NARVI_TOKEN_ENCRYPTION_KEY and base64-decoded + length-validated once
	// here at Load() time — never re-decoded per call (internal/platform.
	// EncryptToken/DecryptToken take this value directly).
	TokenEncryptionKey []byte

	// AllowedEmailDomains, AllowedGitHubOrgs, and AllowedEmails are the 3
	// signup-allowlist mechanisms §13.1 names, each parsed from a
	// comma-separated env var (NARVI_ALLOWED_EMAIL_DOMAINS /
	// NARVI_ALLOWED_GITHUB_ORGS / NARVI_ALLOWED_EMAILS), trimmed, with
	// empty entries dropped. Each is individually optional, but Load fails
	// fast if all three end up empty (see EmptyAllowlistError).
	AllowedEmailDomains []string
	AllowedGitHubOrgs   []string
	AllowedEmails       []string

	// InitialAdminEmails is the first-run-seeding initial-admin list
	// (§13.4: "initial admins set by config"), parsed the same
	// comma-separated way from NARVI_INITIAL_ADMIN_EMAILS. Optional — a
	// verified sign-in email found here gets role "admin" at creation
	// time instead of the enum's own "member" default.
	InitialAdminEmails []string

	// ModalBaseURL and ModalAuthToken configure the real
	// internal/adapters/outbound/modal.Provider cmd/control-plane/main.go
	// constructs (Step 21, "e2e happy path"), read from
	// NARVI_MODAL_BASE_URL / NARVI_MODAL_AUTH_TOKEN. Both required in
	// every stage — never defaulted (there is no real Modal account
	// reachable from this codebase's own tests/CI, see
	// internal/adapters/outbound/modal/doc.go; a real value must be
	// supplied by whoever deploys this binary against an actual Modal
	// account, or a mock standing in for one, e.g. Step 27's own future
	// Prism-based mock server).
	ModalBaseURL   string
	ModalAuthToken string

	// ModalEgressProxyURL optionally routes all Modal traffic through an
	// egress proxy (§4.1), read from NARVI_MODAL_EGRESS_PROXY_URL. Empty
	// (the default) means a direct connection.
	ModalEgressProxyURL string

	// OpenCodeRuntimeVersion is Step 26's ("image builds") own
	// RuntimeVersion fingerprint input, read from
	// NARVI_OPENCODE_RUNTIME_VERSION. Optional: defaults to
	// defaultOpenCodeRuntimeVersion (see that constant's own doc comment
	// for the residual drift risk against .github/workflows/ci.yml's own
	// separate pin).
	OpenCodeRuntimeVersion string

	// LinearWebhookSecret, LinearOAuthClientID, and LinearOAuthClientSecret
	// are Step 34's ("Linear ingress", §8.10) own Linear wiring, read from
	// NARVI_LINEAR_WEBHOOK_SECRET / NARVI_LINEAR_CLIENT_ID /
	// NARVI_LINEAR_CLIENT_SECRET respectively — see those env var names'
	// own doc comments above for why each is a separate secret from its
	// same-shaped-sounding GitHub/HMAC counterpart. All three required in
	// every stage — never defaulted.
	LinearWebhookSecret     string
	LinearOAuthClientID     string
	LinearOAuthClientSecret string

	// LinearDefaultRepoName and LinearDefaultRepoURL are the single repo
	// every Linear-originated session's Repos field is populated with —
	// see linearDefaultRepoNameEnvVarName's own doc comment for the scope
	// note this stopgap exists under. Both required, no default.
	LinearDefaultRepoName string
	LinearDefaultRepoURL  string

	// SlackSigningSecret and SlackBotToken configure Step 33's ("Slack
	// ingress") real Slack Events API adapter, read from
	// NARVI_SLACK_SIGNING_SECRET / NARVI_SLACK_BOT_TOKEN. Both required in
	// every stage -- never defaulted (see slackSigningSecretEnvVarName's
	// own doc comment above for the full reasoning).
	SlackSigningSecret string
	SlackBotToken      string

	// SlackDefaultRepoName and SlackDefaultRepoURL are the single repo a
	// brand-new Slack-spawned session's CreateSessionRequest carries,
	// read from NARVI_SLACK_DEFAULT_REPO_NAME / NARVI_SLACK_DEFAULT_REPO_URL.
	// Both optional -- see slackDefaultRepoNameEnvVarName's own doc
	// comment above for why, and for what leaving either one empty means.
	SlackDefaultRepoName string
	SlackDefaultRepoURL  string

	// AnthropicAPIKey, IntentClassifierProvider, and IntentClassifierModel
	// configure Step 36's ("intent classifier", §8.3/§18) real Anthropic
	// adapter, read from NARVI_ANTHROPIC_API_KEY /
	// NARVI_INTENT_CLASSIFIER_PROVIDER / NARVI_INTENT_CLASSIFIER_MODEL. All
	// three required in every stage -- never defaulted. See
	// anthropicAPIKeyEnvVarName's own doc comment above for the model
	// choice's full reasoning. AnthropicAPIKey is never logged anywhere.
	AnthropicAPIKey          string
	IntentClassifierProvider string
	IntentClassifierModel    string

	// IntentClassifierActiveSurfaces is the permanent shadow-vs-active
	// gating config §18.5/§9.4 requires: "shadow mode ... permanently
	// available, never a one-time launch gate". Parsed the same comma-
	// separated way as AllowedEmailDomains/AllowedGitHubOrgs/AllowedEmails
	// above, from NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES -- each entry
	// one of sessions.spawn_source's own values ("web", "slack", "linear",
	// "github"). A surface NOT listed here defaults to shadow mode (§18.5:
	// "never silently flip a surface to active without an explicit config
	// value saying so") -- optional, an empty/unset value means every
	// surface runs in shadow.
	IntentClassifierActiveSurfaces []string
}

// Load reads process configuration and validates it fail-fast, returning
// named, structured errors (joined via errors.Join when more than one
// check fails) instead of letting an invalid config boot silently. Callers
// (cmd/control-plane/main.go) call this once at process start.
func Load() (*Config, error) {
	stage := Stage(os.Getenv(envVarName))

	var errs []error

	switch stage {
	case StageDevelopment, StageStaging, StageProduction:
		// valid
	case "":
		// Required, no default -- see envVarName's own doc comment: a
		// deploy that forgets to set this must fail to boot, not silently
		// boot as StageDevelopment (which would weaken every auth cookie's
		// Secure attribute, authcookie.go).
		errs = append(errs, &MissingRequiredEnvError{EnvVar: envVarName})
	default:
		errs = append(errs, &InvalidStageError{Value: string(stage)})
	}

	timeouts := DefaultTimeouts()
	if err := timeouts.Validate(); err != nil {
		errs = append(errs, err)
	}

	rawLogLevel := os.Getenv(logLevelEnvVarName)
	if rawLogLevel == "" {
		rawLogLevel = defaultLogLevelValue
	}
	logLevel, err := parseLogLevel(rawLogLevel)
	if err != nil {
		errs = append(errs, err)
	}

	databaseURL := os.Getenv(databaseURLEnvVarName)
	if databaseURL == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: databaseURLEnvVarName})
	}

	httpAddr := os.Getenv(httpAddrEnvVarName)
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}

	hmacSandboxSecret := os.Getenv(hmacSandboxSecretEnvVarName)
	if hmacSandboxSecret == "" {
		errs = append(errs, &InvalidHMACSecretError{EnvVar: hmacSandboxSecretEnvVarName})
	}

	hmacBotsSecret := os.Getenv(hmacBotsSecretEnvVarName)
	if hmacBotsSecret == "" {
		errs = append(errs, &InvalidHMACSecretError{EnvVar: hmacBotsSecretEnvVarName})
	}

	hmacWebhookSecret := os.Getenv(hmacWebhookSecretEnvVarName)
	if hmacWebhookSecret == "" {
		errs = append(errs, &InvalidHMACSecretError{EnvVar: hmacWebhookSecretEnvVarName})
	}

	gitHubClientID := os.Getenv(gitHubClientIDEnvVarName)
	if gitHubClientID == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: gitHubClientIDEnvVarName})
	}

	gitHubClientSecret := os.Getenv(gitHubClientSecretEnvVarName)
	if gitHubClientSecret == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: gitHubClientSecretEnvVarName})
	}

	publicBaseURL := os.Getenv(publicBaseURLEnvVarName)
	if publicBaseURL == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: publicBaseURLEnvVarName})
	}

	gitHubWebhookSecret := os.Getenv(gitHubWebhookSecretEnvVarName)
	if gitHubWebhookSecret == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: gitHubWebhookSecretEnvVarName})
	}

	gitHubBotHandle := os.Getenv(gitHubBotHandleEnvVarName)
	if gitHubBotHandle == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: gitHubBotHandleEnvVarName})
	}

	gitHubBotToken := os.Getenv(gitHubBotTokenEnvVarName)
	if gitHubBotToken == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: gitHubBotTokenEnvVarName})
	}

	// gitHubImageBuildToken is DELIBERATELY OPTIONAL -- see its own env-var
	// doc comment above. No MissingRequiredEnvError is ever appended for
	// it; an empty value here is a valid, expected, degraded-gracefully
	// configuration, not a boot-time failure.
	gitHubImageBuildToken := os.Getenv(gitHubImageBuildTokenEnvVarName)

	var tokenEncryptionKey []byte
	rawTokenEncryptionKey := os.Getenv(tokenEncryptionKeyEnvVarName)
	if rawTokenEncryptionKey == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: tokenEncryptionKeyEnvVarName})
	} else {
		decoded, decodeErr := base64.StdEncoding.DecodeString(rawTokenEncryptionKey)
		switch {
		case decodeErr != nil:
			errs = append(errs, &InvalidTokenEncryptionKeyError{
				Reason: fmt.Sprintf("not valid base64: %v", decodeErr),
			})
		case len(decoded) != tokenEncryptionKeyByteLength:
			errs = append(errs, &InvalidTokenEncryptionKeyError{
				Reason: fmt.Sprintf("must base64-decode to exactly %d bytes for AES-256-GCM, got %d", tokenEncryptionKeyByteLength, len(decoded)),
			})
		default:
			tokenEncryptionKey = decoded
		}
	}

	allowedEmailDomains := parseCommaSeparatedList(os.Getenv(allowedEmailDomainsEnvVarName))
	allowedGitHubOrgs := parseCommaSeparatedList(os.Getenv(allowedGitHubOrgsEnvVarName))
	allowedEmails := parseCommaSeparatedList(os.Getenv(allowedEmailsEnvVarName))
	if len(allowedEmailDomains) == 0 && len(allowedGitHubOrgs) == 0 && len(allowedEmails) == 0 {
		errs = append(errs, &EmptyAllowlistError{})
	}

	initialAdminEmails := parseCommaSeparatedList(os.Getenv(initialAdminEmailsEnvVarName))

	modalBaseURL := os.Getenv(modalBaseURLEnvVarName)
	if modalBaseURL == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: modalBaseURLEnvVarName})
	}

	modalAuthToken := os.Getenv(modalAuthTokenEnvVarName)
	if modalAuthToken == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: modalAuthTokenEnvVarName})
	}

	modalEgressProxyURL := os.Getenv(modalEgressProxyURLEnvVarName)

	openCodeRuntimeVersion := os.Getenv(openCodeRuntimeVersionEnvVarName)
	if openCodeRuntimeVersion == "" {
		openCodeRuntimeVersion = defaultOpenCodeRuntimeVersion
	}

	linearWebhookSecret := os.Getenv(linearWebhookSecretEnvVarName)
	if linearWebhookSecret == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: linearWebhookSecretEnvVarName})
	}

	linearOAuthClientID := os.Getenv(linearOAuthClientIDEnvVarName)
	if linearOAuthClientID == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: linearOAuthClientIDEnvVarName})
	}

	linearOAuthClientSecret := os.Getenv(linearOAuthClientSecretEnvVarName)
	if linearOAuthClientSecret == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: linearOAuthClientSecretEnvVarName})
	}

	linearDefaultRepoName := os.Getenv(linearDefaultRepoNameEnvVarName)
	if linearDefaultRepoName == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: linearDefaultRepoNameEnvVarName})
	}

	linearDefaultRepoURL := os.Getenv(linearDefaultRepoURLEnvVarName)
	if linearDefaultRepoURL == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: linearDefaultRepoURLEnvVarName})
	}

	slackSigningSecret := os.Getenv(slackSigningSecretEnvVarName)
	if slackSigningSecret == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: slackSigningSecretEnvVarName})
	}

	slackBotToken := os.Getenv(slackBotTokenEnvVarName)
	if slackBotToken == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: slackBotTokenEnvVarName})
	}

	// SlackDefaultRepoName/URL are optional (see their own env-var-name
	// doc comment above) -- but a NON-empty value is validated with the
	// SAME domain validators the REST create-session path already trusts
	// (internal/adapters/inbound/httpapi.CreateSessionCore), so a typo'd
	// operator-configured repo fails fast at boot rather than surfacing
	// as a confusing 500 on the first Slack mention that tries to use it.
	slackDefaultRepoName := os.Getenv(slackDefaultRepoNameEnvVarName)
	if slackDefaultRepoName != "" {
		if err := reposource.ValidateRepoName(slackDefaultRepoName); err != nil {
			errs = append(errs, fmt.Errorf("invalid %s: %w", slackDefaultRepoNameEnvVarName, err))
		}
	}
	slackDefaultRepoURL := os.Getenv(slackDefaultRepoURLEnvVarName)
	if slackDefaultRepoURL != "" {
		if err := reposource.ValidateRepoURL(slackDefaultRepoURL); err != nil {
			errs = append(errs, fmt.Errorf("invalid %s: %w", slackDefaultRepoURLEnvVarName, err))
		}
	}

	anthropicAPIKey := os.Getenv(anthropicAPIKeyEnvVarName)
	if anthropicAPIKey == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: anthropicAPIKeyEnvVarName})
	}

	intentClassifierProvider := os.Getenv(intentClassifierProviderEnvVarName)
	if intentClassifierProvider == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: intentClassifierProviderEnvVarName})
	}

	intentClassifierModel := os.Getenv(intentClassifierModelEnvVarName)
	if intentClassifierModel == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: intentClassifierModelEnvVarName})
	}

	intentClassifierActiveSurfaces := parseCommaSeparatedList(os.Getenv(intentClassifierActiveSurfacesEnvVarName))

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return &Config{
		Stage:                  stage,
		Timeouts:               timeouts,
		LogLevel:               logLevel,
		DatabaseURL:            databaseURL,
		HTTPAddr:               httpAddr,
		HMACSandboxSecret:      hmacSandboxSecret,
		HMACBotsSecret:         hmacBotsSecret,
		HMACWebhookSecret:      hmacWebhookSecret,
		GitHubClientID:         gitHubClientID,
		GitHubClientSecret:     gitHubClientSecret,
		GitHubWebhookSecret:    gitHubWebhookSecret,
		GitHubBotHandle:        gitHubBotHandle,
		GitHubBotToken:         gitHubBotToken,
		GitHubImageBuildToken:  gitHubImageBuildToken,
		PublicBaseURL:          publicBaseURL,
		TokenEncryptionKey:     tokenEncryptionKey,
		AllowedEmailDomains:    allowedEmailDomains,
		AllowedGitHubOrgs:      allowedGitHubOrgs,
		AllowedEmails:          allowedEmails,
		InitialAdminEmails:     initialAdminEmails,
		ModalBaseURL:           modalBaseURL,
		ModalAuthToken:         modalAuthToken,
		ModalEgressProxyURL:    modalEgressProxyURL,
		OpenCodeRuntimeVersion: openCodeRuntimeVersion,

		LinearWebhookSecret:     linearWebhookSecret,
		LinearOAuthClientID:     linearOAuthClientID,
		LinearOAuthClientSecret: linearOAuthClientSecret,
		LinearDefaultRepoName:   linearDefaultRepoName,
		LinearDefaultRepoURL:    linearDefaultRepoURL,

		SlackSigningSecret:   slackSigningSecret,
		SlackBotToken:        slackBotToken,
		SlackDefaultRepoName: slackDefaultRepoName,
		SlackDefaultRepoURL:  slackDefaultRepoURL,

		AnthropicAPIKey:          anthropicAPIKey,
		IntentClassifierProvider: intentClassifierProvider,
		IntentClassifierModel:    intentClassifierModel,

		IntentClassifierActiveSurfaces: intentClassifierActiveSurfaces,
	}, nil
}
