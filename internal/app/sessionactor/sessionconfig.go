// This file (sessionconfig.go) implements real SessionConfig assembly
// (Step 21, "e2e happy path", design decision 6) -- the FIRST real caller
// anywhere in the repo that constructs a sessionconfig.SessionConfig
// struct literal (confirmed by a repo-wide grep before this Step started).

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	appreviewtriage "github.com/khazaddev/narvi/internal/app/reviewtriage"
	"github.com/khazaddev/narvi/internal/domain/provenance"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// publicWsBaseURL derives a ws(s):// base URL from httpBaseURL (platform.
// Config.PublicBaseURL, an http(s):// URL) by swapping the scheme
// (http->ws, https->wss) and keeping everything else -- the same
// derive-rather-than-add-a-field choice internal/sandboxagent/credentials.
// NewCPClient already makes in the opposite direction (ws/wss ->
// http/https). Documented alternative NOT taken: a second, separately
// configured platform.Config field (e.g. PublicWsBaseURL) -- deriving
// avoids a second config value that could silently drift from
// PublicBaseURL's own host/port.
func publicWsBaseURL(httpBaseURL string) (string, error) {
	parsed, err := url.Parse(httpBaseURL)
	if err != nil {
		return "", fmt.Errorf("sessionactor: parse public base url %q: %w", httpBaseURL, err)
	}

	var wsScheme string
	switch parsed.Scheme {
	case "https":
		wsScheme = "wss"
	case "http":
		wsScheme = "ws"
	default:
		return "", fmt.Errorf("sessionactor: public base url %q has unrecognized scheme %q, want http or https", httpBaseURL, parsed.Scheme)
	}

	return wsScheme + "://" + parsed.Host, nil
}

// reposFromJSON unmarshals sessions.repos' raw JSONB bytes (design
// decision 1, migrations/000018_session_repos.up.sql) into the
// SessionConfig wire shape. Both CreateSessionRequestReposElem (the wire
// shape httpapi.CreateSession originally persisted) and
// SessionConfigReposElem share the identical JSON shape
// ({branch, name, url}), so a direct unmarshal into the SessionConfig
// type is correct -- no intermediate conversion type needed.
func reposFromJSON(raw []byte) ([]sessionconfig.SessionConfigReposElem, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var repos []sessionconfig.SessionConfigReposElem
	if err := json.Unmarshal(raw, &repos); err != nil {
		return nil, fmt.Errorf("sessionactor: unmarshal session repos: %w", err)
	}
	return repos, nil
}

// environmentPathScope resolves the session's own Environment.PathScope
// (§14.1), when one is attached, for threading into the assembled
// SessionConfig's own optional pathScope field (Step 29, "gitstate
// in-sandbox" -- §14.1's own clone-step enforcement needs the sandbox
// process to actually receive these glob patterns, since sandbox-agent is
// a separate process from the control plane and only knows what it's told
// via NARVI_SESSION_CONFIG). Returns (nil, nil) when environmentID is
// invalid (pgtype.UUID's own zero value) -- the overwhelming common,
// unscoped case -- WITHOUT ever querying the environments table at all,
// mirroring contractdrift.go's own checkContractDrift precedent ("no
// environment_id, ... a plain, logged, no-op") for the identical "no
// Environment attached" gate. Uses tx (the SAME already-open transact
// planFreshSpawn/planRestore run this from), not a's own pool, since this
// runs inside their transact, not after it.
func (a *Actor) environmentPathScope(ctx context.Context, tx pgx.Tx, environmentID pgtype.UUID) ([]string, error) {
	if !environmentID.Valid {
		return nil, nil
	}

	env, err := a.stores.environment.WithTx(tx).Get(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: get environment for path scope: %w", err)
	}

	if len(env.PathScope) == 0 {
		// An Environment can be created for its mock_config alone, with no
		// path_scope attached (§14.1: "an optional path_scope ... and an
		// optional mock_config" -- two independent optional attributes,
		// same reasoning as contractdrift.go's own MockConfigured check) --
		// nothing to unmarshal, nothing to report.
		return nil, nil
	}

	var pathScope []string
	if err := json.Unmarshal(env.PathScope, &pathScope); err != nil {
		return nil, fmt.Errorf("sessionactor: unmarshal environment path_scope: %w", err)
	}
	return pathScope, nil
}

// reviewCounterReviewerModel resolves Step 69's own §26.4 opposing-model-
// family override for THIS session, when one applies -- nil (no override
// at all, sessionconfig.SessionConfig.ReviewCounterReviewerModel's own
// documented "no override" zero value) for every session that is not a
// GitHub PR review session at all (pgx.ErrNoRows from GetBySessionID, the
// overwhelming common case: most sessions are ordinary build sessions with
// no github_pr_sessions row), OR that IS a review session but has no
// resolvable authoring-model provenance (a human-authored PR -- see
// reviewtriage.ResolveCounterReviewerModel's own doc comment for why this
// is the common case even among review sessions, not a degraded one), OR
// (B2 fix) has a resolvable authoring model but no OTHER catalog provider
// this session actually has a usable credential for -- see
// reviewCredentialedProviders' own doc comment. Best-effort, NEVER errors
// (mirrors reviewtriage.ResolveProvenance's own "the review depth decision
// this signal rides alongside must never be delayed or blocked by a
// degraded authorship lookup" contract, applied here to session boot
// instead) -- a failed lookup degrades to no override, never a blocked
// spawn (§10-P2: "never block a spawn"). Logs the resolved decision either
// way (B2 fix: "no observability on the counter-reviewer pin") -- the ONE
// place this decision is made, so an operator investigating "why did the
// counter-reviewer run under model X" (or "why didn't it get an opposing
// pin at all") has a single log line to search for, keyed by repo/PR.
func (a *Actor) reviewCounterReviewerModel(ctx context.Context, tx pgx.Tx, sessionRow sqlcgen.Session) *string {
	if a.stores.githubPRSession == nil {
		return nil
	}
	prSession, err := a.stores.githubPRSession.WithTx(tx).GetBySessionID(ctx, sessionRow.ID)
	if err != nil {
		// pgx.ErrNoRows: not a review session at all -- every OTHER error
		// (a genuine, unexpected read failure) degrades identically, never
		// blocking this spawn over a best-effort, purely additive signal.
		return nil
	}

	triageDeps := appreviewtriage.Deps{Artifacts: a.stores.artifact, Sessions: a.stores.session}
	prov := appreviewtriage.ResolveProvenance(ctx, triageDeps, prSession.RepoFullName, prSession.PrNumber)
	if prov.AuthoringModel == "" {
		// The overwhelming common case (human-authored PR, or a Narvi-
		// authored one with no recorded build_model_id) -- nothing to
		// oppose, so skip the credential lookup below entirely rather than
		// paying for a query whose answer could never change this outcome.
		return nil
	}

	credentialedProviders := a.reviewCredentialedProviders(ctx, tx, sessionRow)
	model := appreviewtriage.ResolveCounterReviewerModel(prov.AuthoringModel, credentialedProviders)
	logger := platform.Logger(ctx)
	if model == "" {
		logger.Info("sessionactor: review counter-reviewer: no opposing-model override resolved",
			"repo_full_name", prSession.RepoFullName, "pr_number", prSession.PrNumber,
			"authoring_model", prov.AuthoringModel, "credentialed_providers", credentialedProviders)
		return nil
	}
	logger.Info("sessionactor: review counter-reviewer: pinned opposing model",
		"repo_full_name", prSession.RepoFullName, "pr_number", prSession.PrNumber,
		"authoring_model", prov.AuthoringModel, "counter_reviewer_model", model)
	return &model
}

// reviewCredentialedProviders resolves the set of counterReviewerProviderPreference
// providers (internal/app/reviewtriage) that sessionRow's own repo(s)/
// environment/creator actually has a usable credential for -- B2 fix
// (adversarial review of Step 69, §26.4): "prefer no pin over guessing
// when the opposing provider is not known-credentialed". Mirrors
// httpapi.ProviderCredentialsDelivery's own resolution inputs exactly
// (repoFullNames from sessionRow.Repos via reposource.ParseOwnerRepo,
// environmentID from sessionRow.EnvironmentID, userID from sessionRow.
// CreatedBy) but stops at EXISTENCE (reviewtriage.CredentialedProviders'
// own byProvider-then-Resolve reduction) -- this function never decrypts
// anything, and a.stores.providerCredential's own ValueEncrypted column is
// never even read here.
//
// nil (a.stores.providerCredential == nil, or any read/parse failure) is
// the SAME safe degradation this file's own reviewCounterReviewerModel
// already established for every other best-effort lookup: a nil map read
// is always false in Go, so ResolveCounterReviewerModel's own credential
// gate treats "we could not determine this" identically to "nothing is
// credentialed" -- never a guess, and never a blocked spawn either (§10-P2).
func (a *Actor) reviewCredentialedProviders(ctx context.Context, tx pgx.Tx, sessionRow sqlcgen.Session) map[string]bool {
	logger := platform.Logger(ctx)
	if a.stores.providerCredential == nil {
		return nil
	}

	repoFullNames, err := reviewCredentialRepoFullNames(sessionRow.Repos)
	if err != nil {
		logger.Warn("sessionactor: review counter-reviewer: parse session repos for credential lookup failed", "error", err)
		return nil
	}

	var environmentID *string
	if sessionRow.EnvironmentID.Valid {
		id := sessionRow.EnvironmentID.String()
		environmentID = &id
	}
	var userID *string
	if sessionRow.CreatedBy.Valid {
		id := sessionRow.CreatedBy.String()
		userID = &id
	}

	rows, err := a.stores.providerCredential.WithTx(tx).ListForResolution(ctx, repoFullNames, environmentID, userID)
	if err != nil {
		logger.Warn("sessionactor: review counter-reviewer: list provider credentials for opposing-model gating failed", "error", err)
		return nil
	}
	return appreviewtriage.CredentialedProviders(rows)
}

// reviewCredentialRepoFullNames mirrors httpapi.sessionRepoFullNames
// exactly (providercredentialsdelivery.go) -- duplicated here rather than
// exported cross-package: both unmarshal sessions.repos' own raw JSONB
// bytes and keep only the repos whose clone URL parses via reposource.
// ParseOwnerRepo, skipping (never erroring on) a malformed entry, for the
// SAME ProviderCredentialStore.ListForResolution call each package makes
// on its own session's own repos.
func reviewCredentialRepoFullNames(rawRepos []byte) ([]string, error) {
	if len(rawRepos) == 0 {
		return nil, nil
	}
	var repos []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rawRepos, &repos); err != nil {
		return nil, err
	}
	fullNames := make([]string, 0, len(repos))
	for _, repo := range repos {
		owner, name, err := reposource.ParseOwnerRepo(repo.URL)
		if err != nil {
			continue
		}
		fullNames = append(fullNames, owner+"/"+name)
	}
	return fullNames, nil
}

// assembleSessionConfig builds the real SESSION_CONFIG document (§6.4) a
// freshly spawned OR restored sandbox receives -- design decision 6's own
// exact field mapping:
//
//   - BootMode: the caller-supplied bootMode -- Fresh for a plain spawn
//     (dispatch.go's planFreshSpawn), SnapshotRestore for a restore
//     (dispatch.go's planRestore, Step 22 "snapshots & restore", design
//     decision 6b: "thread a boolean/enum parameter through
//     assembleSessionConfig rather than hardcoding a second copy of this
//     function"). Step 26 ("image builds") upgrades a Fresh value to
//     RepoImage AFTER this function returns (dispatch.go's own
//     resolveAndSetImage, imageresolve.go), once -- and only once -- a
//     real, ready, matching prebuilt image is actually found for that
//     spawn's own fingerprint: internal/domain/sandboxboot.EvaluateHook's
//     own hook policy (§6.4) treats repo_image as "setup.sh already ran at
//     build time and does not run again", which is exactly the case a real
//     prebuilt-image spawn is; reporting Fresh in that case would make
//     sandbox-agent redundantly re-run setup.sh at every boot regardless,
//     defeating the entire point of image prebuilding. A restore's own
//     BootMode is deliberately never upgraded this way (see
//     resolveAndSetImage's own doc comment for why). BootModeBuild remains
//     an unused placeholder even after Step 26 -- ports.SandboxProvider.
//     BuildImage's own signature (§4.1) carries no SessionConfig at all, so
//     there is no SessionConfig for this control plane to ever stamp
//     BootModeBuild onto; that value is reserved for whatever
//     provider-internal (or future) mechanism actually drives a real
//     image-baking boot sequence, out of this Step's own scope since it
//     doesn't go through CreateSandbox/assembleSessionConfig at all.
//   - ControlPlaneWsUrl: publicWsBaseURL(a.publicBaseURL) +
//     "/sessions/{id}/ws?type=sandbox".
//   - CorrelationId: always nil (no ingress webhook exists yet to have
//     minted one -- SessionConfig.CorrelationId's own doc comment: "Null
//     only when no upstream correlation id exists").
//   - Gen: the sandbox row's own just-bumped gen.
//   - PathScope: Step 29's own addition -- environmentPathScope(ctx, tx,
//     sessionRow.EnvironmentID), above; nil (absent from the wire document
//     entirely, via its own omitempty) for the overwhelming common,
//     unscoped case.
//   - Repos: read back from sessions.repos.
//   - SandboxId: sandboxID, the caller's already-known sandboxes.id
//     (row.ID.String() at the one production call site, tryPlanSpawn) --
//     this sandbox's own stable, real identity, closing the env-leak
//     remediation batch's other honest gap: the ONLY channel into the
//     sandbox's own environment (NARVI_SESSION_CONFIG) is now how
//     sandbox-agent learns its own X-Sandbox-ID for the sandbox WS
//     handshake (§6.1), instead of always defaulting to "".
//   - SandboxToken: the freshly minted PLAINTEXT token (never logged).
//   - SessionId: the session's own id string.
func (a *Actor) assembleSessionConfig(
	ctx context.Context, tx pgx.Tx,
	sessionRow sqlcgen.Session, gen int, plaintextToken, sandboxID string, bootMode sessionconfig.SessionConfigBootMode,
) (sessionconfig.SessionConfig, error) {
	wsBase, err := publicWsBaseURL(a.publicBaseURL)
	if err != nil {
		return sessionconfig.SessionConfig{}, err
	}

	repos, err := reposFromJSON(sessionRow.Repos)
	if err != nil {
		return sessionconfig.SessionConfig{}, err
	}

	pathScope, err := a.environmentPathScope(ctx, tx, sessionRow.EnvironmentID)
	if err != nil {
		return sessionconfig.SessionConfig{}, err
	}
	var pathScopeField *sessionconfig.SessionConfigPathScope
	if len(pathScope) > 0 {
		typed := sessionconfig.SessionConfigPathScope(pathScope)
		pathScopeField = &typed
	}

	sessionID := sessionRow.ID.String()
	controlPlaneWsURL := strings.TrimSuffix(wsBase, "/") + "/sessions/" + sessionID + "/ws?type=sandbox"

	return sessionconfig.SessionConfig{
		BootMode:          bootMode,
		ControlPlaneWsUrl: controlPlaneWsURL,
		CorrelationId:     nil,
		Gen:               gen,
		PathScope:         pathScopeField,
		Repos:             repos,
		SandboxId:         sandboxID,
		SandboxToken:      plaintextToken,
		SessionId:         sessionID,
		// CapabilityRestricted (Step 48, §17.2): true exactly for a
		// sentinel-auto-fix child session -- see provenance.
		// IsSentinelAutoFix's own doc comment for the three independent
		// things that key off this SAME provenance_tag value; this is the
		// third: sandbox-agent writes the glob-restricted OpenCode agent
		// config into the workspace before ever spawning `opencode serve`
		// for this ONE kind of session.
		CapabilityRestricted: provenance.IsSentinelAutoFix(sessionRow.ProvenanceTag),
		// ReviewCounterReviewerModel (Step 69, §26.4): nil for every
		// session that either is not a GitHub PR review session at all, or
		// is one but has no resolvable authoring-model provenance to
		// oppose -- see reviewCounterReviewerModel's own doc comment,
		// above, for the full "why nil is the common case, not a
		// degradation".
		ReviewCounterReviewerModel: sessionconfig.SessionConfigReviewCounterReviewerModel(a.reviewCounterReviewerModel(ctx, tx, sessionRow)),
	}, nil
}
