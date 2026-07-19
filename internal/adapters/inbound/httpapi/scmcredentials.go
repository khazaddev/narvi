// This file (scmcredentials.go) implements POST /sessions/{sessionID}/
// scm-credentials (Step 21, "e2e happy path", design decision 8) -- the
// control-plane side of the wire contract internal/sandboxagent/
// credentials.CPClient (Step 15) already built and tested the CLIENT side
// of. See that package's own cpclient.go doc comment: "THE CP ENDPOINT
// THIS TALKS TO DOES NOT EXIST YET... whoever implements Step 21
// reconciles the two sides then." This file is that reconciliation --
// every field name/shape below matches CPClient's own
// scmCredentialsRequest/scmCredentialsResponse exactly, deliberately, not
// coincidentally.
//
// Deliberately mounted OUTSIDE auth.Middleware (Step 20's cookie-based,
// browser-user auth): this is a SANDBOX-bearer-token-authenticated
// endpoint, matching internal/adapters/inbound/wshub/sandbox.go's own
// header-bearer-token handshake precedent from Step 18 exactly, not a
// browser-facing route at all -- see cmd/control-plane/main.go's own
// mounting (outside the /api prefix, alongside the sandbox/client WS
// route, not inside the /api/sessions auth-gated group).
//
// Audit remediation (security-crosscutting & docs-completeness-vs-plan
// lenses): this Step's own original design left two gaps against §5.2
// ("scoped https+host only") and the sandbox-liveness parity
// internal/adapters/inbound/wshub/sandbox.go already enforces on the
// sibling sandbox-WS handshake:
//
//  1. Host-scoping was a no-op (`_ = req.Host`) -- the decrypted GitHub
//     OAuth token was handed back for ANY requested host, not just a host
//     the session's own repos (sessions.repos) actually use. Fixed below
//     by deriving the session's own real repo hosts (sessionRepoHosts)
//     and rejecting (403, the same generic "no usable credential" body)
//     any req.Host that matches none of them.
//  2. No dead-sandbox or gen check existed at all -- unlike wshub's own
//     handshake, a sandbox that terminalized (Stopped/Stale/Failed) kept
//     minting fresh, real credentials off its last-known-live token_hash
//     indefinitely, and no gen fencing existed on this endpoint's request
//     shape whatsoever. Fixed below by mirroring wshub's own
//     IsDeadSandboxStatus check (410, same point in the handshake: right
//     after the sandbox row lookup, before the token comparison) plus a
//     new X-Sandbox-Gen request header, compared against the sandbox
//     row's own Gen (403 on mismatch) -- see the gen-mismatch check inside
//     ScmCredentials below for why this check is deliberately built anyway
//     despite being largely redundant with token_hash's own
//     overwrite-on-respawn behavior.

package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// scmCredentialsRequest mirrors internal/sandboxagent/credentials.
// cpclient.go's own scmCredentialsRequest exactly.
type scmCredentialsRequest struct {
	Host string `json:"host"`
}

// scmCredentialsResponse mirrors internal/sandboxagent/credentials.
// cpclient.go's own scmCredentialsResponse exactly.
type scmCredentialsResponse struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// verifySandboxBearerToken duplicates internal/adapters/inbound/wshub's
// own (unexported) verifySandboxToken logic for the HASH COMPARISON half
// only -- SHA-256 hash (wshub.HashSandboxToken, already exported for
// exactly this reuse) + crypto/subtle.ConstantTimeCompare, never a bare
// `==` (Step 18's own established constant-time-comparison discipline).
//
// Deliberately DOES NOT copy wshub's own nil-token_hash bypass (a sandbox
// row with no token_hash yet minted is treated there as "accept any
// non-empty presented token" -- see internal/adapters/inbound/wshub/
// doc.go's own doc comment on verifySandboxToken). That bypass's blast
// radius on the WS handshake is bounded to gating a connection; a
// successful check HERE hands back a real, decrypted GitHub OAuth access
// token -- a genuine live credential, not just a connection -- so this
// endpoint is deliberately stricter: a nil/absent token_hash is an
// immediate reject, never an implicit accept. Confirmed currently
// unreachable in production (every sandbox-row-creation path this Step's
// own code uses -- tryPlanSpawn's own UpsertSandboxForSpawn call,
// internal/app/sessionactor/dispatch.go -- always sets token_hash), but
// the elevated stakes here mean this endpoint must not inherit that
// bypass regardless of whether it is reachable today.
func verifySandboxBearerToken(presented string, storedHash *string) bool {
	if presented == "" || storedHash == nil {
		return false
	}
	got := wshub.HashSandboxToken(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(*storedHash)) == 1
}

// ScmCredentials backs POST /sessions/{sessionID}/scm-credentials (note:
// no /api prefix -- a sandbox-to-CP endpoint, not a browser-facing REST
// route, §5.2). Outcome table (design decision 8; steps 2 and 4 below are
// this audit remediation's own additions, deliberately placed at the SAME
// points internal/adapters/inbound/wshub/sandbox.go's own sandbox-WS
// handshake places its equivalent checks -- see that file's own doc
// comment steps 7/8):
//
//  1. sessionID does not parse as a UUID, or no sandbox row exists for it
//     -> 404 (mirrors wshub/sandbox.go's own "malformed and nonexistent
//     both mean no such session" precedent -- this caller is
//     sandbox-agent code, never a browser).
//  2. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410 (mirrors
//     wshub's own "session stopped" status/message convention). Checked
//     immediately after the sandbox row lookup, BEFORE the token
//     comparison and gen check below -- same ordering wshub itself uses,
//     since a terminalized sandbox's last-known-live token_hash/gen are
//     no longer meaningful to compare against at all. This closes a real,
//     previously-open window: sandboxes.token_hash is neither rotated nor
//     cleared when a sandbox terminalizes (only overwritten by a later
//     respawn's own UpsertSandboxForSpawn), so before this check a dead
//     sandbox's last-known token could mint fresh, real GitHub credentials
//     indefinitely.
//  3. The presented X-Sandbox-Gen header is missing/malformed, or parses
//     but does not equal sandboxRow.Gen -> 403 (mirrors wshub's own
//     gen-mismatch reasoning, §9.3 scenario #6). Genuinely defense-in-depth
//     rather than the primary fix for an otherwise-open hole: sandboxes.
//     token_hash is overwritten on every respawn (UpsertSandboxForSpawn,
//     internal/app/sessionactor/dispatch.go), so an old gen's sandbox
//     token already implicitly fails the token comparison in step 5 below
//     once a respawn has happened. Built anyway to match wshub's own
//     parity and this endpoint's own elevated stakes (a live external
//     credential, not just a connection) -- never assume token_hash
//     overwrite alone is sufficient for every future code path.
//  4. Authorization: Bearer <token> missing/malformed, or the presented
//     token fails verifySandboxBearerToken -> 401.
//  5. Malformed request body -> 400.
//  6. req.Host does not case-insensitively match ANY host among the
//     session's own repos (sessions.repos, via sessionRepoHosts) -> 403,
//     the SAME generic body as step 7 below (§5.2: "scoped https+host
//     only" -- the decrypted token must never be handed back for a host
//     the session's own repos don't actually use, e.g. a compromised
//     in-sandbox dependency probing an arbitrary host via the
//     credential-helper protocol).
//  7. The session's own created_by is NULL, OR that user has no linked
//     identities row for provider=github, OR that identity's
//     access_token_encrypted is NULL, OR platform.DecryptToken fails on
//     it -> 403. These four (plus step 6's host-scoping failure above)
//     are deliberately grouped as ONE outcome class ("no usable OAuth
//     credential is available for this session/host") -- the honest "no
//     bot/service-account fallback exists" gap named in this Step's own
//     brief, not a bug to work around by inventing a fake bot credential
//     (§8.11's own fallback half is explicitly out of scope). 403 (not
//     500): this is an authorization-shaped absence from the caller's
//     perspective, not a server malfunction, and mirrors auth.Middleware's
//     own generic-rejection-body discipline (never distinguishing WHICH
//     sub-case applied, in the response body -- an enumeration-hardening
//     precedent this package already established at Step 20). Step 3's
//     gen-mismatch reuses the SAME 403 status code but is logged
//     separately server-side (see the handler body) so it stays
//     observable without adding a caller-visible distinction.
//  8. Otherwise -> 200 with scmCredentialsResponse{Username:
//     "x-access-token", Password: <decrypted token>, ExpiresAt: now +
//     timeouts.ScmCredentialTTL}.
//
// The decrypted token is NEVER logged, at any point, under any
// circumstance -- grepped for in this Step's own diff before reporting
// done, exactly like every prior security-sensitive Step's own discipline.
func ScmCredentials(
	sessions *postgres.SessionStore,
	sandboxes *postgres.SandboxStore,
	identities *postgres.IdentityStore,
	tokenEncryptionKey []byte,
	timeouts platform.Timeouts,
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
			logger.Error("httpapi: scm-credentials: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Dead-sandbox check FIRST, before the gen/token comparisons below
		// -- same ordering wshub/sandbox.go's own handshake uses (see this
		// func's own doc comment step 2 for why). A Suspect sandbox is
		// deliberately NOT dead (IsDeadSandboxStatus is a deny-list, not an
		// allow-list) -- it must still be able to mint credentials.
		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		// Gen check (this func's own doc comment step 3): defense-in-depth
		// alongside the token comparison below, not the primary fix for a
		// distinct hole -- see that doc comment for the full reasoning.
		// Missing/malformed gen is treated identically to a mismatch (403,
		// the same generic body every other sub-case in this class uses):
		// a well-formed caller (CPClient.Fetch, updated by this same audit
		// remediation to always send it) always presents one, so there is
		// no legitimate case to distinguish here.
		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: scm-credentials: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req scmCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: scm-credentials: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Host-scoping check (this func's own doc comment step 6, §5.2
		// "scoped https+host only"): req.Host must match at least one of
		// the session's own real repo hosts before ANY credential is ever
		// minted for it.
		hasHost, err := sessionHasRepoHost(sessionRow.Repos, req.Host)
		if err != nil {
			logger.Error("httpapi: scm-credentials: parse session repos failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !hasHost {
			logger.Warn("httpapi: scm-credentials: rejecting: requested host not among session's own repo hosts",
				"requested_host", req.Host)
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		if !sessionRow.CreatedBy.Valid {
			logger.Warn("httpapi: scm-credentials: session has no created_by user; no bot fallback exists (§8.11)")
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		identity, err := identities.GetByUserAndProvider(ctx, sessionRow.CreatedBy, sqlcgen.IdentityProviderGithub)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Error("httpapi: scm-credentials: get identity failed", "error", err)
			} else {
				logger.Warn("httpapi: scm-credentials: user has no github identity")
			}
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		if identity.AccessTokenEncrypted == nil {
			logger.Warn("httpapi: scm-credentials: github identity has no stored access token")
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		plaintext, err := platform.DecryptToken(tokenEncryptionKey, identity.AccessTokenEncrypted)
		if err != nil {
			// The decrypt error itself is safe to log (it never contains
			// the ciphertext or plaintext -- see platform.DecryptToken's
			// own doc comment), but the plaintext token it would have
			// produced is NEVER logged, here or anywhere else.
			logger.Error("httpapi: scm-credentials: decrypt access token failed", "error", err)
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		writeJSON(w, http.StatusOK, scmCredentialsResponse{
			Username:  "x-access-token",
			Password:  string(plaintext),
			ExpiresAt: time.Now().Add(timeouts.ScmCredentialTTL),
		})
	}
}

// bearerTokenFromHeader extracts the bearer token from r's Authorization
// header -- mirrors internal/adapters/inbound/wshub/sandbox.go's own
// bearerToken function exactly (that one is unexported to its own
// package; duplicated here rather than exported cross-package for such a
// tiny, dependency-free helper).
func bearerTokenFromHeader(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

// sessionRepoHosts unmarshals rawRepos (sessions.repos' own raw JSONB
// bytes, sqlcgen.Session.Repos) and returns the lowercased host of each
// repo whose Url parses successfully -- audit remediation (security-
// crosscutting & docs-completeness-vs-plan lenses), design decision 1.
//
// This is a local, httpapi-package equivalent of internal/app/sessionactor/
// sessionconfig.go's own (unexported, cross-package-unreachable)
// reposFromJSON: same JSON shape ({branch, name, url}), same generated
// wire TYPE (sessionconfig.SessionConfigReposElem, reused for the type
// only), but its own separate function -- httpapi is a different, adjacent
// inbound layer, and importing sessionactor's unexported helper isn't
// possible; exporting it purely for this one cross-layer reuse would risk
// an import-cycle/layering violation this batch's own design explicitly
// avoids.
//
// A repo whose Url fails to parse, or parses with an empty Host, is
// skipped rather than erroring the whole request: sessions.repos is
// already-trusted, already-persisted data by the time this endpoint runs
// (Step 21's CreateSession already accepted/persisted it) -- this is HOST
// COMPARISON against an already-trusted list, not input validation of a
// new untrusted value, so one defensively-malformed entry should not fail
// credential minting for every OTHER, well-formed repo host the session
// does have.
func sessionRepoHosts(rawRepos []byte) ([]string, error) {
	if len(rawRepos) == 0 {
		return nil, nil
	}
	var repos []sessionconfig.SessionConfigReposElem
	if err := json.Unmarshal(rawRepos, &repos); err != nil {
		return nil, fmt.Errorf("httpapi: unmarshal session repos: %w", err)
	}
	hosts := make([]string, 0, len(repos))
	for _, repo := range repos {
		parsed, err := url.Parse(repo.Url)
		if err != nil || parsed.Host == "" {
			continue
		}
		hosts = append(hosts, strings.ToLower(parsed.Host))
	}
	return hosts, nil
}

// sessionHasRepoHost reports whether host case-insensitively matches at
// least one of the hosts sessionRepoHosts derives from rawRepos -- ordinary
// HTTP host-header comparison convention (lowercase both sides), per this
// batch's own design decision 1.
func sessionHasRepoHost(rawRepos []byte, host string) (bool, error) {
	hosts, err := sessionRepoHosts(rawRepos)
	if err != nil {
		return false, err
	}
	host = strings.ToLower(host)
	for _, h := range hosts {
		if h == host {
			return true, nil
		}
	}
	return false, nil
}
