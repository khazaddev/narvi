// This file (scmcredentials.go) implements POST /sessions/{sessionID}/
// scm-credentials (§9.3, "e2e happy path", design decision 8) -- the
// control-plane side of the wire contract internal/sandboxagent/
// credentials.CPClient (§6.4) already built and tested the CLIENT side
// of. See that package's own cpclient.go doc comment: "THE CP ENDPOINT
// THIS TALKS TO DOES NOT EXIST YET... whoever builds the real endpoint
// reconciles the two sides then." This file is that reconciliation --
// every field name/shape below matches CPClient's own
// scmCredentialsRequest/scmCredentialsResponse exactly, deliberately, not
// coincidentally.
//
// Deliberately mounted OUTSIDE auth.Middleware (§13.1's cookie-based,
// browser-user auth): this is a SANDBOX-bearer-token-authenticated
// endpoint, matching internal/adapters/inbound/wshub/sandbox.go's own
// header-bearer-token handshake precedent from §3.2 exactly, not a
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
//
// A later, separate audit finding (M8, cross-step): this handler is the
// EARLIER half of the same push_complete chain internal/app/sessionactor's
// own pushpr.go createPRBestEffort forms the LATER half of -- both mint/use
// the session creator's stored GitHub OAuth token, off the SAME session,
// often mere seconds apart (this endpoint hands the sandbox a credential to
// `git push` with; createPRBestEffort then opens the PR once that push
// completes). createPRBestEffort's own creatorMayGetPRAttribution
// (internal/app/sessionactor/githubtoken.go) already re-checks the
// creator's CURRENT role and disabled flag fresh, right before using their
// token, with an explicit staleness rationale: a session can outlive the
// moment its creator was disabled or demoted. This handler had NO such
// check at all -- it decrypted and handed back the creator's real token to
// any caller holding a valid sandbox bearer token, even one whose creator
// an admin had JUST disabled (e.g. offboarding, incident response) or
// demoted to viewer mid-turn. Fixed below by re-checking the creator's
// CURRENT row (Disabled, then Role) immediately after the created_by-NULL
// check, before ever looking up their identity/token -- the SAME §13.3
// viewer-guard threshold creatorMayGetPRAttribution itself uses, not a
// different one invented for this endpoint (403, the same generic body as
// every other outcome in this class -- see step 8 below).
//
// A still later audit sweep (cross-step, cross-package) found TWO MORE
// call sites inside internal/app/sessionactor itself -- contractdrift.go's
// checkContractDrift and imageresolve.go's resolveAndSetImage -- carrying
// this SAME gap, this batch never having touched either. Rather than a
// third and fourth inline copy of the identical Disabled-then-Role recheck,
// that check is now sessionactor.CheckCreatorGuard (githubtoken.go), an
// EXPORTED function all four call sites -- this handler included -- call
// directly: this package already imports internal/app/sessionactor for
// Registry/EnsureDispatched, so sharing this one further check across that
// existing dependency is a smaller, safer surface than a third bespoke
// copy would have been. This handler's own step 8 (below) now reads
// sessionactor.CheckCreatorGuard's verdict rather than re-deriving it from
// a locally fetched sqlcgen.User row -- see that function's own doc
// comment for the complete rationale, and this handler's own body for how
// its verdict maps onto THIS endpoint's own status codes (a genuine,
// unexpected GetByID failure is now a 500, matching sessions.Get/
// sandboxes.Get's own established discipline elsewhere in this same
// handler, distinct from the expected "row absent" 403 -- a nitpick this
// same audit sweep also raised: this handler's own first version of this
// check logged Warn and returned 403 unconditionally on ANY GetByID
// failure, never distinguishing the two).
//
// Audit remediation ("server-side verdict", §8.2/§5.2 confirmed
// finding): a REVIEW session (one with a github_pr_sessions row,
// reviewverdict.go's own identical reverse-lookup precedent) never pushes
// or opens a PR -- it only clones a PR's head branch read-only, for inline
// code-review context (§8.2), and its own output reaches GitHub
// exclusively through the verdict-posting tool (reviewverdict.go,
// §8.2), which authenticates with cfg.GitHubBotToken, never a per-commenter
// OAuth token. Handing such a session's SANDBOX the session CREATOR's own
// broadly `repo`-scoped personal GitHub OAuth token (steps 7-9 below) for
// this exact purpose was itself a confirmed credential-exposure gap: an
// arbitrary human whose ONLY interaction with Narvi was commenting on a PR
// had their own full, cross-repo, cross-org personal credential cached to
// the reviewing sandbox's local disk (internal/sandboxagent/credentials.
// Cache) merely because a review session happened to need SOME credential
// to clone with -- a far broader blast radius than this endpoint's own
// host-scoping (step 6) or per-session repo list ever intended to expose.
// Fixed below (the NEW step 7, checked immediately after host-scoping,
// before the creator/identity lookups steps 8-10 exist to serve): a review
// session mints botToken instead, skipping the creator-guard/identity
// path entirely -- the SAME single, statically-configured bot credential
// already trusted to read (internal/app/reviewcontext.Fetch) and post
// (githubapi.VerdictNotifier) on this exact repo/PR, never the creator's
// own identity. This does not claim to make a bash-capable review agent
// structurally incapable of ever calling GitHub's API directly with
// SOME credential (that would require OS-level process isolation between
// sandbox-agent and the agent runtime it supervises, or GitHub App
// fine-grained/read-only installation tokens -- neither of which this
// codebase has today, see this Step's own PR description) -- it closes
// the STRICTLY WORSE half of that gap: an arbitrary commenter's own
// broad, personal, cross-repo credential never reaches a review sandbox
// at all.

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
	"github.com/khazaddev/narvi/internal/app/sessionactor"
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
// `==` (§3.2's own established constant-time-comparison discipline).
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
// comment steps 7/8; step 9 below is the M8 audit finding's own addition,
// calling internal/app/sessionactor's own CheckCreatorGuard; step 7 below
// is this audit remediation's own addition):
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
//     the SAME generic body as steps 8-10 below (§5.2: "scoped https+host
//     only" -- the decrypted token must never be handed back for a host
//     the session's own repos don't actually use, e.g. a compromised
//     in-sandbox dependency probing an arbitrary host via the
//     credential-helper protocol).
//  7. This session has a github_pr_sessions row (prSessions.
//     GetBySessionID succeeds) -> 200 with botToken, never the creator's
//     own identity -- see this file's own top comment ("Audit remediation")
//     for the full rationale. steps 8-10 below (the
//     creator-guard/identity/decrypt path) are skipped entirely for a
//     review session: they exist to find and gate a PER-USER OAuth
//     credential, which a review session has no legitimate use for at
//     all. A genuine, unexpected error from GetBySessionID OTHER than
//     "no such row" (pgx.ErrNoRows) is a 500, matching this handler's own
//     established "row absent vs genuine failure" discipline (step 9's
//     identical distinction, and sessions.Get/sandboxes.Get above).
//  8. The session's own created_by is NULL -> 403, the SAME generic body
//     as steps 6/9/10 -- no bot/service-account fallback exists (§8.11),
//     nothing further to even check once a session has no creator at all.
//  9. OR the session's own created_by user is now Disabled, OR their Role
//     is now viewer -> 403, the SAME generic body as steps 6/8/10 (M8 audit
//     finding: re-checked FRESH here, right before step 10's identity/token
//     lookup even begins -- not the role/disabled state as of session
//     creation). Calls internal/app/sessionactor's own CheckCreatorGuard
//     (githubtoken.go) -- the SAME §13.3 viewer-guard threshold
//     creatorMayGetPRAttribution itself uses, via the SAME shared function
//     (not a separately-maintained copy) -- see this file's own top
//     comment for the full "why this endpoint needs the identical
//     staleness recheck the later PR-creation step of the same
//     push_complete chain already performs" rationale. A missing user row
//     (should be unreachable -- created_by is a real FK) is treated the
//     SAME as this outcome class's other sub-cases: no usable credential,
//     nothing to fail loudly over -- 403, same as Disabled/Viewer. A
//     GENUINE, unexpected failure fetching that row is instead a 500,
//     matching how sessions.Get/sandboxes.Get above already treat a real
//     DB failure (only the row's clean absence is folded into this 403
//     class, not any other lookup failure).
//  10. OR (regardless of step 9 passing): that user has no linked
//     identities row for provider=github, OR that identity's
//     access_token_encrypted is NULL, OR platform.DecryptToken fails on
//     it -> 403. These, plus step 6's host-scoping failure, step 8's
//     no-creator failure, and step 9's disabled/demoted failure above, are
//     deliberately grouped as ONE outcome class ("no usable OAuth
//     credential is available for this session/host") -- the honest "no
//     bot/service-account fallback exists" gap named in this Step's own
//     brief, not a bug to work around by inventing a fake bot credential
//     (§8.11's own fallback half is explicitly out of scope). 403 (not
//     500): this is an authorization-shaped absence from the caller's
//     perspective, not a server malfunction, and mirrors auth.Middleware's
//     own generic-rejection-body discipline (never distinguishing WHICH
//     sub-case applied, in the response body -- an enumeration-hardening
//     precedent this package already established at §13.1). §5.3's
//     gen-mismatch reuses the SAME 403 status code but is logged
//     separately server-side (see the handler body) so it stays
//     observable without adding a caller-visible distinction.
//  11. Otherwise -> 200 with scmCredentialsResponse{Username:
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
	users *postgres.UserStore,
	prSessions *postgres.GitHubPRSessionStore,
	botToken string,
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

		// Review-session check (this func's own doc comment step 7,
		// audit remediation): a session with a github_pr_sessions row
		// never pushes/opens a PR -- see this file's own top comment for
		// the full "why the creator's own personal OAuth token has no
		// legitimate use here" rationale. Mints botToken directly,
		// skipping steps 8-10's creator-guard/identity/decrypt path
		// entirely -- that path exists to find a PER-USER credential,
		// which is exactly what this branch avoids handing to a review
		// sandbox at all.
		if _, err := prSessions.GetBySessionID(ctx, sessionID); err == nil {
			writeJSON(w, http.StatusOK, scmCredentialsResponse{
				Username:  "x-access-token",
				Password:  botToken,
				ExpiresAt: time.Now().Add(timeouts.ScmCredentialTTL),
			})
			return
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Error("httpapi: scm-credentials: get github pr session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if !sessionRow.CreatedBy.Valid {
			logger.Warn("httpapi: scm-credentials: session has no created_by user; no bot fallback exists (§8.11)")
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		// Creator disabled/role recheck (this func's own doc comment step
		// 8, M8 audit finding): re-read the creator's CURRENT row fresh,
		// right here, before ever looking up their identity/token below --
		// calls internal/app/sessionactor's own CheckCreatorGuard
		// (githubtoken.go), the SAME shared function creatorMayGetPRAttribution
		// itself now calls (same Disabled-then-Role order, same §13.3
		// viewer-guard threshold), which already gates the LATER
		// PR-creation step of this SAME push_complete chain. A session can
		// outlive the moment its creator was disabled or demoted -- that
		// function's own doc comment's full rationale applies here
		// identically, since this endpoint mints the very credential that
		// later push uses.
		//
		// A genuine, unexpected GetByID failure (Err set, ErrNotFound
		// false) is a 500 -- matching sessions.Get/sandboxes.Get's own
		// established discipline above, and this SAME handler's own
		// identities.GetByUserAndProvider handling five lines below --
		// distinct from the expected "row absent" case (Err set,
		// ErrNotFound true), which denies exactly like Disabled/Viewer:
		// this endpoint's own first version of this check logged Warn and
		// returned 403 unconditionally for ANY GetByID failure, never
		// distinguishing the two (a nitpick this same audit sweep raised).
		guard := sessionactor.CheckCreatorGuard(ctx, users, sessionRow.CreatedBy)
		if guard.Err != nil {
			if !guard.ErrNotFound {
				logger.Error("httpapi: scm-credentials: get session creator for disabled/role recheck failed",
					"error", guard.Err, "user_id", sessionRow.CreatedBy.String())
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			logger.Warn("httpapi: scm-credentials: session creator row not found for disabled/role recheck; denying",
				"user_id", sessionRow.CreatedBy.String())
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}
		if guard.Disabled {
			logger.Warn("httpapi: scm-credentials: session creator is now disabled; refusing credential (audit M8, §13.3 viewer guard parity)",
				"user_id", sessionRow.CreatedBy.String())
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}
		if guard.Viewer {
			logger.Warn("httpapi: scm-credentials: session creator is now a viewer; refusing credential (audit M8, §13.3 viewer guard parity)",
				"user_id", sessionRow.CreatedBy.String())
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
// (§9.3's CreateSession already accepted/persisted it) -- this is HOST
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
