// This file (sessionconfig.go) implements real SessionConfig assembly
// (Step 21, "e2e happy path", design decision 6) -- the FIRST real caller
// anywhere in the repo that constructs a sessionconfig.SessionConfig
// struct literal (confirmed by a repo-wide grep before this Step started).

package sessionactor

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

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

// assembleSessionConfig builds the real SESSION_CONFIG document (§6.4) a
// freshly spawned OR restored sandbox receives -- design decision 6's own
// exact field mapping:
//
//   - BootMode: the caller-supplied bootMode -- Fresh for a plain spawn
//     (dispatch.go's planFreshSpawn), SnapshotRestore for a restore
//     (dispatch.go's planRestore, Step 22 "snapshots & restore", design
//     decision 6b: "thread a boolean/enum parameter through
//     assembleSessionConfig rather than hardcoding a second copy of this
//     function"). BootModeBuild/BootModeRepoImage stay unused placeholders
//     (Step 26's own job).
//   - ControlPlaneWsUrl: publicWsBaseURL(a.publicBaseURL) +
//     "/sessions/{id}/ws?type=sandbox".
//   - CorrelationId: always nil (no ingress webhook exists yet to have
//     minted one -- SessionConfig.CorrelationId's own doc comment: "Null
//     only when no upstream correlation id exists").
//   - Gen: the sandbox row's own just-bumped gen.
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

	sessionID := sessionRow.ID.String()
	controlPlaneWsURL := strings.TrimSuffix(wsBase, "/") + "/sessions/" + sessionID + "/ws?type=sandbox"

	return sessionconfig.SessionConfig{
		BootMode:          bootMode,
		ControlPlaneWsUrl: controlPlaneWsURL,
		CorrelationId:     nil,
		Gen:               gen,
		Repos:             repos,
		SandboxId:         sandboxID,
		SandboxToken:      plaintextToken,
		SessionId:         sessionID,
	}, nil
}
