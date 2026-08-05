// This file (providercredentialsdelivery.go) implements Step 53's own
// ("provider credential injection", §25.1/§25.3) CP-side DELIVERY
// endpoint for sandbox-agent: POST /sessions/{sessionID}/provider-
// credentials (note: no /api prefix, exactly like scm-credentials/
// snapshot/review-verdict -- a sandbox-to-CP endpoint, not a browser-
// facing REST route, §5.2).
//
// Mirrors scmcredentials.go's own exact security posture, deliberately,
// not coincidentally: sandbox-bearer-token-authenticated (mounted OUTSIDE
// auth.Middleware entirely, alongside the other sandbox-facing routes,
// cmd/control-plane/main.go), the SAME dead-sandbox check
// (sandbox.IsDeadSandboxStatus, 410 -- checked immediately after the
// sandbox row lookup, before the gen/token comparisons) and X-Sandbox-Gen
// fencing (403 on a missing/mismatched header) at the SAME points in the
// handshake, and the SAME bearer-token verification
// (verifySandboxBearerToken, constant-time hash compare, no nil-token_hash
// bypass). A provider-credential leak to a stale/terminalized sandbox is
// exactly the same class of risk scmcredentials.go's own audit history
// already fixed once for SCM tokens (that file's own top doc comment,
// "Audit remediation... design decision 2") -- this endpoint does not
// reintroduce that gap for a new secret type. Outcome table below mirrors
// that file's own numbered list, adapted (no request body/host to check
// here at all -- there is no "host" concept for a provider API key, unlike
// a git credential):
//
//  1. sessionID does not parse as a UUID -> 404.
//  2. Authorization: Bearer <token> missing/malformed -> 401 (checked
//     before the sandbox row lookup, matching scmcredentials.go's own
//     ordering).
//  3. No sandbox row exists for sessionID -> 404.
//  4. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410.
//  5. X-Sandbox-Gen missing/malformed/mismatched -> 403.
//  6. The presented token fails verifySandboxBearerToken -> 401.
//  7. Otherwise -> 200 with a plain, provider-keyed map of PLAINTEXT
//     values, e.g. {"anthropic": "sk-..."} -- resolved via
//     internal/domain/providercredential.Resolve over every candidate row
//     (across all 3 scopes) that could apply to this session's own
//     repo(s)/environment, decrypted server-side (the ONLY layer that
//     ever holds cfg.TokenEncryptionKey). A provider with nothing
//     configured at any scope is simply ABSENT from the map -- never a
//     null/empty-string entry, and never itself an error: the overwhelming
//     common case is zero rows configured for any given session, and this
//     endpoint degrades to an empty {} exactly as gracefully as a fully-
//     configured one.
//
// sandbox-agent (internal/sandboxagent/credentials' own CPClient.
// FetchProviderCredentials, and ultimately cmd/sandbox-agent/main.go's own
// spawn-time wiring) maps each provider name onto its own env-var name(s)
// (providercredential.EnvVarNames) -- deliberately NOT done here, so this
// response's own shape stays a plain, provider-keyed map, and the "1
// provider -> N env vars" mapping detail (google's own 3-name case) lives
// in exactly ONE place, the domain package, never duplicated between CP
// and sandbox-agent.
//
// A single row that fails to decrypt (a genuinely corrupted/tampered
// ciphertext -- platform.DecryptToken's own AES-GCM authentication tag
// catching it) is logged loudly and simply OMITTED from the response,
// never turned into a 500 for the whole request: 3 independent provider
// credentials can resolve in one call, and one corrupted row must not
// deny a sandbox-agent the other 2 it has nothing wrong with. The
// decrypted value itself is NEVER logged, at any point -- matching
// tokenencrypt.go's own "never log plaintext, key, or ciphertext"
// discipline exactly (grepped for in this Step's own diff before
// reporting done).

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/providercredential"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// providerCredentialsResponse is this Step's own invented, documented
// response shape: a plain map from provider name ("google"/"anthropic"/
// "openai") to its resolved PLAINTEXT credential value. Mirrors
// scmCredentialsResponse's own "small, explicit, invented wire shape"
// precedent (scmcredentials.go) -- internal/sandboxagent/credentials'
// own client-side type must match this exactly, the same reconciliation
// scmcredentials.go's own top doc comment describes for the SCM case.
type providerCredentialsResponse struct {
	Credentials map[string]string `json:"credentials"`
}

// sessionRepoFullNames unmarshals rawRepos (sessions.repos' own raw JSONB
// bytes) and returns the "owner/repo" full name of each repo whose clone
// URL parses successfully via reposource.ParseOwnerRepo -- the SAME
// natural key repo_settings.repo_full_name/provider_credentials'
// own repo-scoped rows already use. A repo whose URL fails to parse is
// skipped rather than erroring the whole request -- mirrors
// sessionRepoHosts' own identical "already-trusted, already-persisted
// data; one malformed entry must not fail lookups for every other,
// well-formed repo" reasoning (scmcredentials.go).
func sessionRepoFullNames(rawRepos []byte) ([]string, error) {
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

// ProviderCredentialsDelivery backs POST /sessions/{sessionID}/
// provider-credentials -- see this file's own top doc comment for the
// full outcome table and security-posture rationale.
func ProviderCredentialsDelivery(
	sessions *postgres.SessionStore,
	sandboxes *postgres.SandboxStore,
	providerCredentials *postgres.ProviderCredentialStore,
	tokenEncryptionKey []byte,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var sessionID pgtype.UUID
		if err := sessionID.Scan(chi.URLParam(r, "sessionID")); err != nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		ctx = platform.WithSessionID(ctx, sessionID.String())
		logger := platform.Logger(ctx)

		token, ok := bearerTokenFromHeader(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or malformed authorization header")
			return
		}

		sandboxRow, err := sandboxes.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: provider-credentials: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Dead-sandbox check FIRST, before the gen/token comparisons below
		// -- same ordering scmcredentials.go/wshub/sandbox.go both use.
		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: provider-credentials: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable provider credential for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: provider-credentials: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		repoFullNames, err := sessionRepoFullNames(sessionRow.Repos)
		if err != nil {
			logger.Error("httpapi: provider-credentials: parse session repos failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var environmentID *string
		if sessionRow.EnvironmentID.Valid {
			id := sessionRow.EnvironmentID.String()
			environmentID = &id
		}

		rows, err := providerCredentials.ListForResolution(ctx, repoFullNames, environmentID)
		if err != nil {
			logger.Error("httpapi: provider-credentials: list candidates failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		byProvider := make(map[sqlcgen.ProviderCredentialProvider][]providercredential.Candidate[sqlcgen.ProviderCredential], len(rows))
		for _, row := range rows {
			byProvider[row.Provider] = append(byProvider[row.Provider], providercredential.Candidate[sqlcgen.ProviderCredential]{
				Scope: providercredential.Scope(row.Scope),
				Value: row,
			})
		}

		credentials := make(map[string]string, len(byProvider))
		for provider, candidates := range byProvider {
			winner, ok := providercredential.Resolve(candidates)
			if !ok {
				continue
			}
			plaintext, err := platform.DecryptToken(tokenEncryptionKey, winner.ValueEncrypted)
			if err != nil {
				// Never logs the ciphertext/plaintext -- see
				// platform.DecryptToken's own doc comment. Logged and
				// SKIPPED, not a 500 for the whole request -- see this
				// file's own top doc comment for why.
				logger.Error("httpapi: provider-credentials: decrypt failed",
					"error", err, "provider", string(provider), "scope", string(winner.Scope))
				continue
			}
			credentials[string(provider)] = string(plaintext)
		}

		writeJSON(w, http.StatusOK, providerCredentialsResponse{Credentials: credentials})
	}
}
