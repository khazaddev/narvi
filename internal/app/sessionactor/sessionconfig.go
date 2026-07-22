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
	}, nil
}
