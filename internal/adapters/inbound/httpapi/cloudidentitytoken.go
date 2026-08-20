// This file (cloudidentitytoken.go) implements §27.3's own ("cloud
// identity: OIDC issuer, bindings, minting", §27.3) CP-side MINTING
// endpoint for sandbox-agent: POST /sessions/{sessionID}/cloud-identity-
// token (note: no /api prefix, exactly like scm-credentials/provider-
// credentials/sandbox-secrets -- a sandbox-to-CP endpoint, not a
// browser-facing REST route, §5.2).
//
// Mirrors providercredentialsdelivery.go's own exact security posture,
// deliberately, not coincidentally: sandbox-bearer-token-authenticated
// (mounted OUTSIDE auth.Middleware entirely, alongside the other
// sandbox-facing routes, cmd/control-plane/main.go), the SAME dead-
// sandbox check (sandbox.IsDeadSandboxStatus, 410 -- checked immediately
// after the sandbox row lookup, before the gen/token comparisons) and
// X-Sandbox-Gen fencing (403 on a missing/mismatched header) at the SAME
// points in the handshake, and the SAME bearer-token verification
// (verifySandboxBearerToken, constant-time hash compare, no
// nil-token_hash bypass). Outcome table, adapted from that file's own
// (no host to check here either, but a REQUEST BODY -- the requested
// audience -- that provider-credentials delivery does not have):
//
//  1. sessionID does not parse as a UUID -> 404.
//  2. Authorization: Bearer <token> missing/malformed -> 401 (checked
//     before the sandbox row lookup, matching scmcredentials.go's own
//     ordering).
//  3. No sandbox row exists for sessionID -> 404.
//  4. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410 ("minting
//     stops at dead-sandbox/410, like every other delivery endpoint",
//     §27.3's own explicit closing-paragraph statement).
//  5. X-Sandbox-Gen missing/malformed/mismatched -> 403.
//  6. The presented token fails verifySandboxBearerToken -> 401.
//  7. cloud identity federation not configured (platform.Config.
//     CloudIdentityIssuerURL unset) -> 503, fail-closed (§27.3).
//  8. Malformed request body / blank audience -> 400.
//  9. No binding (environment-scoped OR global-scoped, for this
//     session's own Environment) declares the requested audience -> 403
//     -- "CP refuses any audience no binding for this session's
//     Environment (or global fallback) declares -- it never mints
//     arbitrary-audience tokens" (§27.3, verbatim). THIS is the audience
//     allowlist check -- see resolveCloudIdentityBindingForAudience's own
//     doc comment, and the mutation test pinning it.
//  10. No active signing key configured (nobody has ever called
//     RotateCloudIdentitySigningKey) -> 503, fail-closed.
//  11. Otherwise -> 200 with a signed RS256 JWT: `sub` =
//     cloudidentity.Sub(session's own environment_id) -- STABLE,
//     per-Environment, never session-varying (§27.3) -- `aud` = the
//     requested (now-confirmed-allowed) audience, `exp` ~=
//     platform.Timeouts.CloudIdentityTokenLifetime from now, plus
//     session_id/gen/repos/provenance_tag as CUSTOM claims only (never
//     folded into `sub` -- internal/domain/cloudidentity.BuildClaims'
//     own doc comment, and this Step's own gap-3 discussion for why that
//     distinction matters most for Azure).
//
// A session with no attached Environment (sessionRow.EnvironmentID
// invalid) is refused entirely (403), never falling back to an
// empty-string `sub` against a global-scoped binding: narvi:environment:
// with nothing after the colon is not a value any customer's real
// cloud-side trust policy could ever have been written against (see
// MintCloudIdentityToken's own inline comment at the exact point this is
// decided, and the dedicated test pinning the refusal).

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/oidcsigning"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/cloudidentity"
	"github.com/khazaddev/narvi/internal/domain/providercredential"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// mintCloudIdentityTokenRequest is this endpoint's own hand-written
// request shape -- deliberately NOT wire-contract-generated (contracts/
// rest/v1), mirroring providerCredentialsResponse's own "invented,
// documented wire shape" precedent for every sandbox-bearer delivery
// endpoint in this package (§6.3's own contracts scope is BFF-facing
// browser routes, never this family).
type mintCloudIdentityTokenRequest struct {
	Audience string `json:"audience"`
}

// mintCloudIdentityTokenResponse is this endpoint's own response shape.
type mintCloudIdentityTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// resolveCloudIdentityBindingForAudience is the audience ALLOWLIST check
// §27.3 requires ("CP refuses any audience no binding for this session's
// Environment (or global fallback) declares"): fetches every candidate
// binding (global, plus environmentID's own if non-empty) whose audience
// equals requestedAudience, then resolves environment-vs-global via
// internal/domain/providercredential.Resolve (environment shadows global,
// the SAME priority map sandbox_secrets already reuses -- see
// internal/domain/cloudidentity's own scope.go doc comment). ok is false
// when NO candidate matches -- the caller (below) turns that into a 403,
// never minting a token for an audience nothing declared. The winning
// candidate's own Params never appear in the minted token's own claims
// (internal/domain/cloudidentity.Claims carries no kind/params field at
// all) -- but its Kind IS read past this point, by the caller, as the
// cloud_identity_mint_total metric's own "kind" attribute (see
// recordCloudIdentityMint's own doc comment, cloudidentitymetrics.go) --
// the SAME reason queries/cloudidentitybindings.sql's own
// ListCloudIdentityBindingsForResolution doc comment already gives for
// resolving a winner at all ("to pick a deterministic winner for
// observability").
func resolveCloudIdentityBindingForAudience(ctx context.Context, bindings *postgres.CloudIdentityBindingStore, requestedAudience string, environmentID *string) (sqlcgen.CloudIdentityBinding, bool, error) {
	rows, err := bindings.ListForResolution(ctx, requestedAudience, environmentID)
	if err != nil {
		return sqlcgen.CloudIdentityBinding{}, false, err
	}
	candidates := make([]providercredential.Candidate[sqlcgen.CloudIdentityBinding], 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, providercredential.Candidate[sqlcgen.CloudIdentityBinding]{
			Scope: cloudIdentityBindingScopeToDomainScope(row.Scope),
			Value: row,
		})
	}
	winner, ok := providercredential.Resolve(candidates)
	return winner, ok, nil
}

// MintCloudIdentityToken backs POST /sessions/{sessionID}/cloud-identity-
// token -- see this file's own top doc comment for the full outcome
// table and security-posture rationale.
func MintCloudIdentityToken(
	sessions *postgres.SessionStore,
	sandboxes *postgres.SandboxStore,
	bindings *postgres.CloudIdentityBindingStore,
	signingKeys *postgres.OIDCSigningKeyStore,
	tokenEncryptionKey []byte,
	issuerURL string,
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
			logger.Error("httpapi: cloud-identity-token: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Dead-sandbox check FIRST, before the gen/token comparisons below
		// -- same ordering scmcredentials.go/providercredentialsdelivery.go
		// both use, and the exact behavior §27.3's own closing paragraph
		// requires ("minting stops at dead-sandbox/410, like every other
		// delivery endpoint").
		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: cloud-identity-token: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable cloud identity token for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		// Fail-closed: the whole capability is off when the issuer URL is
		// unset (§27.3) -- checked AFTER the handshake above, matching
		// this codebase's own general posture that a fail-closed
		// capability gate is a service-level fact, not a per-caller
		// authorization fact, so it never leaks ahead of confirming the
		// caller is even a legitimate sandbox for this session.
		if issuerURL == "" {
			writeError(w, http.StatusServiceUnavailable, "cloud identity federation is not configured")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req mintCloudIdentityTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		if err := cloudidentity.ValidateAudience(req.Audience); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: cloud-identity-token: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var environmentID *string
		if sessionRow.EnvironmentID.Valid {
			id := sessionRow.EnvironmentID.String()
			environmentID = &id
		}

		// A session with no attached Environment cannot mint a
		// meaningful `sub` at all -- cloudidentity.Sub("") would produce
		// the literal string "narvi:environment:", which is never a
		// value any real customer trust policy names (every Sub call
		// this codebase makes elsewhere is against a real environments.id
		// -- see internal/domain/cloudidentity.Sub's own doc comment).
		// Refused outright, BEFORE even checking whether a global binding
		// would otherwise have matched -- an environment-less session
		// minting against a global binding would still carry an HONEST
		// but USELESS sub no cloud role could ever be configured to
		// trust, so there is no legitimate case this refusal excludes.
		if environmentID == nil {
			writeError(w, http.StatusForbidden, "session has no attached Environment -- cloud identity tokens require one")
			return
		}

		winner, matched, err := resolveCloudIdentityBindingForAudience(ctx, bindings, req.Audience, environmentID)
		if err != nil {
			logger.Error("httpapi: cloud-identity-token: resolve binding failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !matched {
			// The audience allowlist check (§27.3: "CP refuses any
			// audience no binding for this session's Environment (or
			// global fallback) declares -- it never mints
			// arbitrary-audience tokens"). 403, not 404 -- the session
			// and its handshake are entirely legitimate; only the
			// REQUESTED audience is refused.
			writeError(w, http.StatusForbidden, "no cloud identity binding for this session declares the requested audience")
			return
		}

		activeKey, err := signingKeys.GetActive(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusServiceUnavailable, "no active cloud identity signing key configured")
				return
			}
			logger.Error("httpapi: cloud-identity-token: get active signing key failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		privDER, err := platform.DecryptToken(tokenEncryptionKey, activeKey.PrivateKeyEncrypted)
		if err != nil {
			// Never logs plaintext/ciphertext -- matches
			// platform.DecryptToken's own doc comment.
			logger.Error("httpapi: cloud-identity-token: decrypt signing key failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		priv, err := oidcsigning.DecodePrivateKeyPKCS8(privDER)
		if err != nil {
			logger.Error("httpapi: cloud-identity-token: decode signing key failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		repos, err := sessionRepoFullNames(sessionRow.Repos)
		if err != nil {
			logger.Error("httpapi: cloud-identity-token: parse session repos failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		now := time.Now()
		claims := cloudidentity.BuildClaims(cloudidentity.BuildClaimsInput{
			Issuer:        issuerURL,
			EnvironmentID: *environmentID,
			Audience:      req.Audience,
			IssuedAt:      now,
			Lifetime:      timeouts.CloudIdentityTokenLifetime,
			SessionID:     sessionID.String(),
			Gen:           int64(sandboxRow.Gen),
			Repos:         repos,
			ProvenanceTag: sessionRow.ProvenanceTag,
		})

		signed, err := oidcsigning.Sign(priv, activeKey.Kid, claims)
		if err != nil {
			logger.Error("httpapi: cloud-identity-token: sign failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		expiresAt := time.Unix(claims.Expiry, 0).UTC()

		// cloud-identity-token minting is conjunctive in §27.3: "Minting
		// is logged with correlation_id (§5.3) and counted as a metric".
		// Both halves, HERE, after every fail-closed/allowlist gate above
		// has already passed, never on a refusal/error branch (those each
		// return immediately on their own logger.Error/Warn call above,
		// never reaching this point):
		//
		//  - The LOG line: correlation_id/session_id are already carried
		//    by `logger` (platform.Logger(ctx), fed by
		//    CorrelationIDMiddleware + this handler's own
		//    platform.WithSessionID(ctx, ...) call above) on every field
		//    it emits automatically -- this call adds the fields that
		//    aren't already on ctx (environment id, audience, kid,
		//    expiry). NEVER the token itself -- signed is deliberately
		//    absent from every argument below, matching this file's own
		//    "never logs plaintext/ciphertext" discipline for the signing
		//    key material immediately above.
		//  - The METRIC: cloudIdentityMintTotal (cloudidentitymetrics.go),
		//    incremented once here, tagged by the resolved audience's own
		//    winning binding's Kind for per-cloud breakdown (see
		//    resolveCloudIdentityBindingForAudience's own doc comment for
		//    why reading winner.Kind here, past the allowlist check, is
		//    exactly what that function's own doc comment says it is for).
		logger.Info("httpapi: cloud-identity-token: minted",
			"environment_id", *environmentID,
			"audience", req.Audience,
			"kid", activeKey.Kid,
			"expires_at", expiresAt,
		)
		recordCloudIdentityMint(ctx, string(winner.Kind))

		writeJSON(w, http.StatusOK, mintCloudIdentityTokenResponse{
			Token:     signed,
			ExpiresAt: expiresAt,
		})
	}
}
