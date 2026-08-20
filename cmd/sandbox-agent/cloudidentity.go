// This file (cloudidentity.go) implements §27.4's own ("cloud
// identity: sandbox-side consumption + kubeconfig injection", §27.3) IN-
// SANDBOX consumption half of §27.3's control-plane OIDC issuer:
// discovering which cloud_identity_bindings apply to this session (via
// the new POST /sessions/{id}/cloud-identity-config delivery endpoint,
// internal/sandboxagent/credentials/cloudidentityconfig.go), minting a
// short-lived token per binding (POST /sessions/{id}/cloud-identity-token,
// §27.3's own endpoint, credentials/cloudidentitytoken.go), writing
// each to its own file under cloudIdentityDir, and returning the standard,
// cloud-SDK-documented env vars (internal/domain/cloudidentity.
// ReservedEnvVarNames' own exact names) ready to thread into every
// spawned process -- Spawn/RunBoot/RunHooks' own secretEnv/sandboxSecretEnv
// parameters (§27.1's threading mechanism, extended here, never a new,
// parallel one -- see sandboxsecrets.go's own top doc comment for the
// full "why threading, never os.Setenv" reasoning, which applies to every
// value this file builds identically).
//
// Narvi implements NO per-cloud STS exchange code anywhere in this file
// (or anywhere else) -- every function below either fetches/mints via CP
// or writes a file/env var; the clouds' own SDKs (invoked by whatever the
// spawned process actually is -- opencode serve, a repo's own setup.sh,
// the AWS/GCP/Azure CLIs a customer's own tooling might run) perform the
// actual token exchange, entirely in-sandbox, exactly as §27.3 requires.
//
// # cloudIdentityDir: 0700/0600, never inside a repo tree
//
// cloudIdentityDir ("/narvi/identity") follows boot.ImageManifestPath's/
// openCodeEnvironmentConfigPath's own established "/narvi/..." convention
// -- outside cfg.WorkspaceDir ("/workspace" by default, boot/config.go's
// own defaultWorkspaceDir) entirely, so nothing this file ever writes can
// be committed by a repo's own `git add`. See
// TestCloudIdentityDir_OutsideEveryRepoTree (cloudidentity_test.go) for
// the pinning test.
//
// # Gap 1 (this Step's own spec brief): a mid-session refresh failure
//
// §27.3 specifies refresh at token half-life but says nothing about the
// failure path. The already-on-disk token file stays cryptographically
// valid until its own `exp` (~10 min from when it was minted) -- a real
// window to retry in, which runCloudIdentityRefreshLoop uses (bounded
// retry, timeouts.CloudIdentityTokenMint*, classifyMintTokenError). If
// every retry in that window still fails, this codebase DELETES the
// token file rather than leaving it to simply expire in place. Chosen
// over "leave it" for the reason the brief itself names: an EXPIRED
// token sitting in the well-known file the cloud SDK expects produces a
// confusing, cloud-STS-side failure (a signature/claims-validation
// rejection that reads like almost any other auth misconfiguration --
// wrong role, tampered token, clock skew) well after the fact, whereas a
// MISSING file fails immediately, locally, and unambiguously (an "open
// <path>: no such file or directory" from the SDK's own file read) --
// strictly faster and clearer to diagnose. The SAME deletion policy
// applies uniformly to the INITIAL population path too (a binding whose
// very first mint attempt exhausts its retries never gets a file written
// at all, and contributes no env vars) -- there is no "first mint" vs
// "later refresh" special case; both are "this binding currently has no
// working token", handled identically. The refresh loop itself never
// gives up permanently: every subsequent half-life tick tries again
// regardless of the previous tick's own outcome, so a transient CP
// disruption self-heals on its own next successful mint, matching this
// codebase's "warn and continue" posture throughout (§27.1's own phrase,
// applied here to §27.3).
//
// # Gap 2 (this Step's own spec brief): snapshot restore
//
// cloudIdentityDir lives in the sandbox filesystem, which a
// snapshot_restore boot restores VERBATIM -- so, unlike an ordinary
// fresh/repo_image boot, a restored sandbox's own disk can already
// contain token files (and a kubeconfig, kubeconfig.go) from whatever
// boot last wrote them, potentially minutes, hours, or days stale. Unlike
// §27.1's own OpenCode-config precedent (opencodeconfig.go's own
// applyOpenCodeConfig, which keeps the last-known-good document on a
// FAILED fetch -- a deliberate, reasoned choice for a config document,
// documented on that function), §27.1's own precedent does NOT apply
// here unmodified: a config document degrades gracefully when stale (it
// is still SOME working configuration); a stale cloud-identity token is
// SHARPER, per this Step's own brief -- it is short-lived, security-
// sensitive credential material that may already be outright EXPIRED (a
// snapshot taken more than ~10 minutes before this restore boot) or, even
// if technically still unexpired, tied to a PRIOR sandbox generation/
// session context a customer's cloud-side trust policy has no reason to
// keep honoring one moment longer than Narvi's own contract promises.
// This codebase resolves this the same way §27.1 resolved its OWN
// version of this class of problem -- "application must be authoritative"
// -- taken further here: resetCloudIdentityDir wipes cloudIdentityDir
// ENTIRELY (not merely the individual files a fresh fetch happens to
// report "still configured", the way applyOpenCodeConfig's own per-scope
// removal works) at the START of EVERY boot, unconditionally, regardless
// of cfg.BootMode, BEFORE any fetch/mint is even attempted -- never
// merely for BootModeSnapshotRestore. This is deliberately NOT a boot-
// mode-conditional special case: every OTHER boot mode's own
// cloudIdentityDir is already empty in practice (a genuinely fresh
// sandbox has never written anything there), so the unconditional wipe
// costs nothing on those paths and makes "no stale token/kubeconfig can
// ever survive into a new boot" a structural property of this function's
// own call ordering, not a fact that depends on correctly recognizing
// which boot modes need it. See TestResetCloudIdentityDir_RemovesStaleFiles
// (cloudidentity_test.go) for the mutation-test-visible pinning of this
// exact property.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/domain/cloudidentity"
	"github.com/khazaddev/narvi/internal/domain/sandboxsecret"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// cloudIdentityDir is where every token file, the generated GCP
// credential-config JSON, and (kubeconfig.go) the rendered kubeconfig all
// live -- OUTSIDE cfg.WorkspaceDir, never a repo tree (this file's own top
// doc comment). 0700, matching §27.3's own explicit "/narvi/identity/
// (0700/0600...)" requirement.
const cloudIdentityDir = "/narvi/identity"

// Per-kind file names under cloudIdentityDir -- one OIDC token file per
// cloud_identity_bindings Kind actually resolved for this session (never
// more than 4: aws/gcp/azure/generic), plus one generated GCP external-
// account credential-config JSON (gcpCredentialConfigFileName) whose
// credential_source.file points at gcpTokenFileName.
const (
	awsTokenFileName            = "aws-token"
	gcpTokenFileName            = "gcp-token"
	gcpCredentialConfigFileName = "gcp-credential-config.json"
	azureTokenFileName          = "azure-token"
	genericTokenFileName        = "generic-token"
)

// gcpSTSTokenURL is GCP's own fixed, documented Security Token Service
// endpoint for external_account credential exchange -- not a Narvi value,
// never configurable per-binding (every GCP workload-identity-federation
// setup uses this exact, single endpoint).
const gcpSTSTokenURL = "https://sts.googleapis.com/v1/token"

// gcpSubjectTokenType is RFC 8693's own token-type identifier for a plain
// JWT, GCP's own documented value for an OIDC-token-sourced external
// account.
const gcpSubjectTokenType = "urn:ietf:params:oauth:token-type:jwt"

// resetCloudIdentityDir wipes dir entirely and recreates it empty, 0700 --
// this Step's own gap-2 resolution (see this file's own top doc comment).
// Called unconditionally, once, at the very start of this mechanism's own
// boot-time work, before any fetch/mint is attempted -- the production
// call site (run(), main.go) always passes the cloudIdentityDir constant;
// dir is a parameter (never resolved/hardcoded internally) purely so this
// function stays testable against a temp directory, mirroring
// applyOpenCodeConfig's own identical "homeDir/environmentConfigPath are
// passed in... so this function stays testable" precedent
// (opencodeconfig.go). A failure to remove an existing directory is
// logged (Warn) and non-fatal -- matches this feature's "warn and
// continue, never a boot failure" posture throughout; the subsequent
// MkdirAll/WriteFile calls each fail (and are themselves logged,
// non-fatally) on their own if the directory is left in a bad state,
// rather than this function itself aborting the boot.
func resetCloudIdentityDir(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("sandbox-agent: remove stale cloud identity directory failed, a restored/prior boot's own files may survive", "path", dir, "error", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Warn("sandbox-agent: create cloud identity directory failed, cloud identity/kubeconfig injection will not work this boot", "path", dir, "error", err)
	}
}

// fetchCloudIdentityConfig fetches this session's own resolved
// cloud-identity bindings and cluster binding from CP (POST
// /sessions/{id}/cloud-identity-config), retrying a transport error or 5xx
// up to timeouts.CloudIdentityConfigFetchMaxAttempts times -- mirrors
// fetchSandboxSecrets/fetchOpenCodeConfig's own identical best-effort,
// never-fatal-to-boot posture and bounded-retry shape (deliveryretry.go's
// classifyDeliveryFetchError, the SAME classification every other
// delivery-endpoint fetch in this binary already uses).
//
// fetchOK=false means every attempt failed (or the very first one hit a
// terminal 401/403/404/410) -- logged (Warn) and returned alongside the
// zero-value delivery. This session then simply has no cloud-identity env
// vars/kubeconfig injected at all this boot -- exactly as if this feature
// did not exist -- and the caller records that degradation for AGENTS.md
// (run(), main.go), matching sandbox secrets'/OpenCode config's own
// identical "recorded in the boot log and AGENTS.md" requirement.
func fetchCloudIdentityConfig(ctx context.Context, cfg boot.Config, timeouts platform.Timeouts) (delivery credentials.CloudIdentityConfigDelivery, fetchOK bool) {
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeouts.CloudIdentityConfigFetchTimeout)
	if err != nil {
		slog.Warn("sandbox-agent: build cloud-identity-config CP client failed, booting with no cloud identity/kubeconfig injected", "error", err)
		return credentials.CloudIdentityConfigDelivery{}, false
	}

	retryErr := platform.Retry(ctx, timeouts.CloudIdentityConfigFetchMaxAttempts, timeouts.CloudIdentityConfigFetchRetryBaseDelay, timeouts.CloudIdentityConfigFetchRetryMaxDelay, func() error {
		fetchCtx, cancel := context.WithTimeout(ctx, timeouts.CloudIdentityConfigFetchTimeout)
		defer cancel()

		d, fetchErr := client.FetchCloudIdentityConfig(fetchCtx, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen)
		if fetchErr != nil {
			return classifyDeliveryFetchError(fetchErr)
		}
		delivery = d
		return nil
	})
	if retryErr != nil {
		slog.Warn("sandbox-agent: fetch cloud identity config exhausted every retry attempt, booting with no cloud identity/kubeconfig injected", "error", retryErr)
		return credentials.CloudIdentityConfigDelivery{}, false
	}

	slog.Info("sandbox-agent: resolved cloud identity config",
		"binding_count", len(delivery.Bindings), "cluster_configured", delivery.ClusterBinding != nil)
	return delivery, true
}

// mintCloudIdentityToken mints one token for audience via CP's own §27.3
// minting endpoint, retrying a transport error, a 5xx OTHER than 503,
// or any other classifyMintTokenError-retryable outcome up to
// timeouts.CloudIdentityTokenMintMaxAttempts times -- see
// deliveryretry.go's own classifyMintTokenError for the full
// retry-classification rule (this Step's own explicit resolution of "what
// happens on 503/403/a transient failure"). Shared by three callers: this
// file's own populateCloudIdentityTokenFiles (initial, boot-time
// population) and refreshOneBinding (the half-life background refresh),
// AND kubeconfig.go's own applyClusterBinding (the §27.4 AuthKindOIDC
// cluster-binding rung -- adversarial-review HIGH fix replaced that
// rung's original standalone `kube-credential` subcommand with a call
// straight into this SAME function, see applyClusterBinding's own doc
// comment) -- every mint in this binary goes through this ONE function.
//
// ok=false means every attempt failed (or the very first one hit a
// terminal classification) -- logged (Warn, naming the audience -- public,
// customer-configured metadata, never secret, see
// credentials.CPClient.MintCloudIdentityToken's own doc comment) and
// returned alongside the zero-value token. Never logs the minted TOKEN
// itself, at any point, on either the success or failure path.
func mintCloudIdentityToken(ctx context.Context, client credentials.CloudIdentityTokenMinter, sessionID, sandboxToken string, gen int, audience string, timeouts platform.Timeouts) (minted credentials.MintedCloudIdentityToken, ok bool) {
	retryErr := platform.Retry(ctx, timeouts.CloudIdentityTokenMintMaxAttempts, timeouts.CloudIdentityTokenMintRetryBaseDelay, timeouts.CloudIdentityTokenMintRetryMaxDelay, func() error {
		mintCtx, cancel := context.WithTimeout(ctx, timeouts.CloudIdentityTokenMintTimeout)
		defer cancel()

		m, mintErr := client.MintCloudIdentityToken(mintCtx, sessionID, sandboxToken, gen, audience)
		if mintErr != nil {
			return classifyMintTokenError(mintErr)
		}
		minted = m
		return nil
	})
	if retryErr != nil {
		slog.Warn("sandbox-agent: mint cloud identity token exhausted every retry attempt", "audience", audience, "error", retryErr)
		return credentials.MintedCloudIdentityToken{}, false
	}
	return minted, true
}

// cloudIdentityBindingState is what runCloudIdentityRefreshLoop needs to
// keep refreshing one successfully-populated binding -- built by
// populateCloudIdentityTokenFiles, one per binding whose INITIAL mint
// succeeded (a binding whose first mint failed contributes no state --
// there is nothing to refresh yet, and the next attempt is simply the
// next scheduled tick, exactly like every other binding's own steady-
// state behavior).
type cloudIdentityBindingState struct {
	Kind      string
	Audience  string
	TokenPath string
}

// writeTokenFile writes token to path, creating cloudIdentityDir first if
// needed -- 0600, matching §27.3's own explicit file-permission
// requirement. Returns whether the write fully succeeded; any failure is
// logged (Warn) by the CALLER (this function itself stays a plain,
// testable I/O helper with no logging of its own), matching this
// feature's "warn and continue" posture.
func writeTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token), 0o600)
}

// gcpExternalAccountCredentialConfig is the external_account credential-
// config JSON shape GCP's own client libraries document for a file-
// sourced OIDC token (§27.3: "a generated external-account credential-
// config JSON whose credential_source.file is the token file"). Field
// names/shape match GCP's own published external_account schema; NOT
// verified against a live GCP endpoint by this Step (unlike, e.g., 73a's
// own OpenCode-precedence claim, which WAS verified live) -- this is a
// plausible, documented shape this codebase commits to and tests against
// itself, the same honest posture credentials/cpclient.go's own top doc
// comment already established for a different invented-but-undocumented
// wire contract.
type gcpExternalAccountCredentialConfig struct {
	Type                           string              `json:"type"`
	Audience                       string              `json:"audience"`
	SubjectTokenType               string              `json:"subject_token_type"`
	TokenURL                       string              `json:"token_url"`
	CredentialSource               gcpCredentialSource `json:"credential_source"`
	ServiceAccountImpersonationURL string              `json:"service_account_impersonation_url,omitempty"`
}

// gcpCredentialSource is gcpExternalAccountCredentialConfig's own nested
// "where does the subject token come from" shape -- File is the ONLY
// variant this codebase ever produces (§27.3 is explicit: "file-sourced"
// throughout).
type gcpCredentialSource struct {
	File string `json:"file"`
}

// buildGCPExternalAccountJSON renders params (already validated by
// cloudidentity.ValidateParams) and tokenPath into the JSON document
// GOOGLE_APPLICATION_CREDENTIALS must point at. serviceAccountImpersonationURL
// is Google's own documented URL SHAPE for the optional service-account-
// impersonation variant (built from params.ServiceAccountEmail when
// present) -- absent (omitempty) when params carries no
// ServiceAccountEmail, the common case (a workload identity pool bound
// directly to one principal, no impersonation hop).
func buildGCPExternalAccountJSON(params cloudidentity.GCPParams, tokenPath string) ([]byte, error) {
	doc := gcpExternalAccountCredentialConfig{
		Type:             "external_account",
		Audience:         params.WorkloadIdentityProvider,
		SubjectTokenType: gcpSubjectTokenType,
		TokenURL:         gcpSTSTokenURL,
		CredentialSource: gcpCredentialSource{File: tokenPath},
	}
	if params.ServiceAccountEmail != "" {
		doc.ServiceAccountImpersonationURL = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + params.ServiceAccountEmail + ":generateAccessToken"
	}
	return json.MarshalIndent(doc, "", "  ")
}

// awsRoleSessionName derives an AWS_ROLE_SESSION_NAME value from
// sessionID -- §27.3's own explicit "(+ session name)" -- for CloudTrail
// auditability (which Narvi session assumed this role), rather than
// leaving the AWS SDK to generate one of its own. "narvi-"+a UUID always
// satisfies AWS's own documented charset (alphanumeric plus =,.@-_) and
// 64-character length bound (6+36=42) -- the truncation below is
// defensive, not expected to ever trigger against a real UUID sessionID.
func awsRoleSessionName(sessionID string) string {
	name := "narvi-" + sessionID
	const maxLen = 64
	if len(name) > maxLen {
		name = name[:maxLen]
	}
	return name
}

// applyCloudIdentityBinding mints binding's own token, writes its token
// file under dir, and returns the "NAME=VALUE" env entries this Kind's
// own consumption mechanism sets (internal/domain/cloudidentity.
// ReservedEnvVarNames' own exact names) -- ok=false (nil env, empty
// tokenPath) on ANY failure along the way (unrecognized kind, malformed
// params, exhausted mint retries, or a token-file write failure): this
// ONE binding contributes nothing to this boot, logged (Warn) at the
// exact point of failure, and every OTHER binding is attempted
// independently regardless (the caller's own loop, below) -- never a
// boot failure. dir is a parameter (never the cloudIdentityDir constant
// referenced directly) purely so this function stays testable against a
// temp directory -- the production call site (populateCloudIdentityTokenFiles,
// below) always passes cloudIdentityDir.
func applyCloudIdentityBinding(ctx context.Context, client credentials.CloudIdentityTokenMinter, sessionID, sandboxToken string, gen int, binding credentials.CloudIdentityConfigBinding, timeouts platform.Timeouts, dir string) (env []string, tokenPath string, ok bool) {
	kind := cloudidentity.Kind(binding.Kind)
	if !cloudidentity.IsValidKind(kind) {
		slog.Warn("sandbox-agent: cloud identity binding has an unrecognized kind, skipping", "kind", binding.Kind)
		return nil, "", false
	}
	if err := cloudidentity.ValidateParams(kind, binding.Params); err != nil {
		slog.Warn("sandbox-agent: cloud identity binding has invalid params, skipping", "kind", binding.Kind, "error", err)
		return nil, "", false
	}

	minted, mintOK := mintCloudIdentityToken(ctx, client, sessionID, sandboxToken, gen, binding.Audience, timeouts)
	if !mintOK {
		return nil, "", false
	}

	switch kind {
	case cloudidentity.KindAWS:
		var p cloudidentity.AWSParams
		_ = json.Unmarshal(binding.Params, &p) // already validated above
		path := filepath.Join(dir, awsTokenFileName)
		if err := writeTokenFile(path, minted.Token); err != nil {
			slog.Warn("sandbox-agent: write aws cloud identity token file failed, skipping", "error", err)
			return nil, "", false
		}
		return []string{
			cloudidentity.EnvVarAWSWebIdentityTokenFile + "=" + path,
			cloudidentity.EnvVarAWSRoleARN + "=" + p.RoleARN,
			cloudidentity.EnvVarAWSRoleSessionName + "=" + awsRoleSessionName(sessionID),
		}, path, true

	case cloudidentity.KindGCP:
		var p cloudidentity.GCPParams
		_ = json.Unmarshal(binding.Params, &p) // already validated above
		path := filepath.Join(dir, gcpTokenFileName)
		if err := writeTokenFile(path, minted.Token); err != nil {
			slog.Warn("sandbox-agent: write gcp cloud identity token file failed, skipping", "error", err)
			return nil, "", false
		}
		configDoc, err := buildGCPExternalAccountJSON(p, path)
		if err != nil {
			slog.Warn("sandbox-agent: build gcp external-account credential config failed, skipping", "error", err)
			return nil, "", false
		}
		configPath := filepath.Join(dir, gcpCredentialConfigFileName)
		if err := writeTokenFile(configPath, string(configDoc)); err != nil {
			slog.Warn("sandbox-agent: write gcp external-account credential config failed, skipping", "error", err)
			return nil, "", false
		}
		return []string{
			cloudidentity.EnvVarGoogleApplicationCredentials + "=" + configPath,
		}, path, true

	case cloudidentity.KindAzure:
		var p cloudidentity.AzureParams
		_ = json.Unmarshal(binding.Params, &p) // already validated above
		path := filepath.Join(dir, azureTokenFileName)
		if err := writeTokenFile(path, minted.Token); err != nil {
			slog.Warn("sandbox-agent: write azure cloud identity token file failed, skipping", "error", err)
			return nil, "", false
		}
		return []string{
			cloudidentity.EnvVarAzureFederatedTokenFile + "=" + path,
			cloudidentity.EnvVarAzureClientID + "=" + p.ClientID,
			cloudidentity.EnvVarAzureTenantID + "=" + p.TenantID,
		}, path, true

	case cloudidentity.KindGeneric:
		var p cloudidentity.GenericParams
		_ = json.Unmarshal(binding.Params, &p) // already validated above
		// Fuller injectable-name check (POSIX shape, not already reserved
		// by ANY mechanism -- NARVI_*/OPENCODE_*/a provider credential/
		// this Step's own fixed AWS/GCP/Azure names/KUBECONFIG) -- see
		// internal/domain/cloudidentity.GenericParams' own doc comment
		// for why this fuller check lives HERE (an import-cycle-free call
		// site) rather than inside ValidateParams itself.
		if err := sandboxsecret.ValidateName(p.EnvVar); err != nil {
			slog.Warn("sandbox-agent: generic cloud identity binding names a non-injectable env var, skipping", "env_var", p.EnvVar, "error", err)
			return nil, "", false
		}
		path := filepath.Join(dir, genericTokenFileName)
		if err := writeTokenFile(path, minted.Token); err != nil {
			slog.Warn("sandbox-agent: write generic cloud identity token file failed, skipping", "error", err)
			return nil, "", false
		}
		return []string{p.EnvVar + "=" + path}, path, true

	default:
		// Unreachable -- IsValidKind above already rejected anything else
		// -- kept as a defensive floor, never expected to execute.
		return nil, "", false
	}
}

// populateCloudIdentityTokenFiles is run()'s own boot-time entry point:
// mints and writes a token file for every binding delivery.Bindings
// names, in Kind order (deterministic, matching CP's own ORDER BY kind),
// and returns the aggregated "NAME=VALUE" env entries ready to thread
// into opencodeproc.Spawn/boot.RunBoot (this file's own top doc comment)
// alongside the per-binding state runCloudIdentityRefreshLoop needs to
// keep refreshing each one. cfg.SessionConfig is assumed non-nil (the
// caller, run(), only ever reaches this inside that same guard every
// OTHER boot-time fetch already requires). dir is threaded straight
// through to applyCloudIdentityBinding -- the production call site
// always passes cloudIdentityDir; a test may pass a temp directory.
func populateCloudIdentityTokenFiles(ctx context.Context, cfg boot.Config, timeouts platform.Timeouts, client credentials.CloudIdentityTokenMinter, bindings []credentials.CloudIdentityConfigBinding, dir string) (env []string, states []cloudIdentityBindingState) {
	for _, binding := range bindings {
		bindingEnv, tokenPath, ok := applyCloudIdentityBinding(ctx, client, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen, binding, timeouts, dir)
		if !ok {
			continue
		}
		env = append(env, bindingEnv...)
		states = append(states, cloudIdentityBindingState{Kind: binding.Kind, Audience: binding.Audience, TokenPath: tokenPath})
	}
	if len(env) > 0 {
		names := make([]string, 0, len(states))
		for _, s := range states {
			names = append(names, s.Kind)
		}
		sort.Strings(names)
		slog.Info("sandbox-agent: populated cloud identity token files", "kinds", names)
	}
	return env, states
}

// runCloudIdentityRefreshLoop refreshes every entry in states at token
// half-life (timeouts.CloudIdentityTokenLifetime/2 -- a COMPUTED interval,
// never a second stored duration, so it can never drift out of sync with
// the lifetime it is literally half of), until ctx is done -- one
// goroutine per binding via errgroup.Group (never a naked `go` statement,
// §11), converging on group.Wait() exactly like bridge.Run's own
// ctx-driven lifetime. A binding list of zero entries (the overwhelming
// common case: no cloud identity binding configured for this session at
// all) is a correct, cheap no-op that still blocks until ctx is done --
// matching run()'s own existing nil-bridge "ctx-wait stand-in" precedent
// exactly, so this function's own caller can unconditionally fold it into
// the SAME errgroup.Group run() already uses for bridge.Run/the ctx-wait
// stand-in, with no special-casing for "nothing to refresh".
func runCloudIdentityRefreshLoop(ctx context.Context, cfg boot.Config, timeouts platform.Timeouts, client credentials.CloudIdentityTokenMinter, states []cloudIdentityBindingState) error {
	if len(states) == 0 {
		<-ctx.Done()
		return nil
	}
	var g errgroup.Group
	for _, state := range states {
		state := state
		g.Go(func() error {
			refreshOneBinding(ctx, cfg, timeouts, client, state)
			return nil
		})
	}
	return g.Wait()
}

// refreshOneBinding wakes every token half-life and re-mints state's own
// token -- on success, overwrites state.TokenPath in place (0600,
// writeTokenFile); on exhausted retries (mintCloudIdentityToken's own
// ok=false), DELETES state.TokenPath instead of leaving a now-expired
// token sitting there -- this Step's own gap-1 resolution, see this
// file's own top doc comment for the full "why deletion, not expiry"
// reasoning. Either outcome, the NEXT tick tries again regardless --
// never a permanent give-up for this binding (this feature's "warn and
// continue" posture is an ONGOING posture, not a one-shot fallback).
// Returns (via ctx.Done()) only when the whole sandbox is shutting down.
func refreshOneBinding(ctx context.Context, cfg boot.Config, timeouts platform.Timeouts, client credentials.CloudIdentityTokenMinter, state cloudIdentityBindingState) {
	interval := timeouts.CloudIdentityTokenLifetime / 2
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			minted, ok := mintCloudIdentityToken(ctx, client, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen, state.Audience, timeouts)
			if !ok {
				slog.Warn("sandbox-agent: cloud identity token refresh exhausted every retry attempt, removing the now-unrefreshable token file",
					"kind", state.Kind, "path", state.TokenPath)
				if err := os.Remove(state.TokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					slog.Warn("sandbox-agent: remove stale cloud identity token file failed", "kind", state.Kind, "path", state.TokenPath, "error", err)
				}
				continue
			}
			if err := writeTokenFile(state.TokenPath, minted.Token); err != nil {
				slog.Warn("sandbox-agent: write refreshed cloud identity token file failed", "kind", state.Kind, "path", state.TokenPath, "error", err)
				continue
			}
			slog.Info("sandbox-agent: refreshed cloud identity token", "kind", state.Kind, "expires_at", minted.ExpiresAt)
		}
	}
}
