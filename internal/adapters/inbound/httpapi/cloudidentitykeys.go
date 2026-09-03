// This file (cloudidentitykeys.go) implements §27.3's own ("cloud
// identity: OIDC issuer, bindings, minting", §27.3) admin-triggered
// signing-key ROTATION endpoint -- POST /api/cloud-identity/signing-keys/
// rotate. This is this Step's own gap-2 resolution: §27.3 specifies
// rotation's shape (overlapping-validity, §5.2's own sandbox-token
// rotation precedent) but never what initiates it. §27.8 (this same
// technical plan's own "risks and open questions" section) resolves this
// explicitly: "manual, admin-triggered rotation with the overlap window
// is v1; automatic scheduled rotation is deferred until operational
// experience says what cadence is right." See internal/domain/oidckey's
// own doc comment for the FULL "why manual, admin-triggered, mirroring
// §5.2's sandbox-token precedent" reasoning -- this file is just the
// thin HTTP handler over that already-justified design.
//
// Gated by authz.ActionManageCloudIdentityKeys (admin ONLY, not even
// maintainer -- see that action's own doc comment for why this sits one
// row stricter than binding CRUD: rotating the issuer's own signing key
// is a platform-wide posture change affecting every Environment's
// currently-mintable tokens at once, unlike a binding change which
// affects one Environment).
//
// Fails closed exactly like every other cloud-identity surface when the
// capability is off (platform.Config.CloudIdentityIssuerURL unset,
// §27.3): rotating a signing key nobody can ever discover via JWKS (the
// issuer is unset, so OIDCJWKS itself responds 503) would be pointless
// busywork at best, and confusing at worst -- refused up front instead,
// with the SAME 503 status (oidcdiscovery.go's own doc comment has the
// full "why 503, matching uploadmint.go's own precedent" reasoning). The
// gate itself lives ONE level up, at the route group
// (cmd/control-plane/main.go's own r.Use(httpapi.
// RequireCloudIdentityCapability(cfg.CloudIdentityIssuerURL))) -- this
// handler carries no inline issuerURL check of its own, mirroring the two
// cloud-identity-bindings route groups (cloudidentitybindings.go), so
// there is exactly one place per request path that decides "is cloud
// identity on" (see that middleware's own doc comment).

package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/oidcsigning"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// RotateCloudIdentitySigningKey backs POST /api/cloud-identity/
// signing-keys/rotate -- see this file's own top doc comment for the
// full design. The capability-off fail-closed gate (§27.3) is enforced
// ONE level up, by cmd/control-plane/main.go's own
// r.Use(httpapi.RequireCloudIdentityCapability(...)) on this route's own
// group -- this handler carries no issuerURL parameter or inline check of
// its own (see RequireCloudIdentityCapability's own doc comment for why).
// pool/keys/auditLog/tokenEncryptionKey/timeouts are threaded through
// exactly like every other admin-tx-writing handler in this package
// (mirrors UpdateMemberRole's own begin/defer-rollback/commit shape).
// Reads the wall clock directly (time.Now()) -- httpapi is an
// inbound ADAPTER, not /internal/domain, so §11's domain-purity rule does
// not apply here; mirrors OIDCJWKS' own identical choice (that handler's
// own doc comment has the full "why no clock injection is needed for
// testing" reasoning, which applies here symmetrically:
// postgres.OIDCSigningKeyStore.Rotate itself takes an explicit `now`
// parameter, so a test can assert exact retiredAt/publishableUntil
// arithmetic by calling THIS real handler and reading its own response,
// bounding the observed instant against time.Now() taken immediately
// before/after the call rather than needing to inject a fixed one).
func RotateCloudIdentitySigningKey(pool *pgxpool.Pool, keys *postgres.OIDCSigningKeyStore, auditLog *postgres.AuditLogStore, tokenEncryptionKey []byte, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageCloudIdentityKeys, authz.Resource{}) {
			return
		}

		ctx := r.Context()
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		// Key material is generated BEFORE the transaction opens -- RSA
		// keygen is CPU-bound, not DB-bound, and there is no reason to
		// hold a Postgres transaction open across it (mirrors
		// scmcredentials.go's own "do the expensive/non-DB work first,
		// keep the transaction short" precedent elsewhere in this
		// package).
		priv, err := oidcsigning.GenerateKeyPair()
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: generate key pair failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		kid, err := oidcsigning.GenerateKid()
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: generate kid failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		privDER, err := oidcsigning.EncodePrivateKeyPKCS8(priv)
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: encode private key failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// Never logs privDER/privEncrypted/priv at any point -- matching
		// tokenencrypt.go's own "never log plaintext, key, or ciphertext"
		// discipline exactly.
		privEncrypted, err := platform.EncryptToken(tokenEncryptionKey, privDER)
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: encrypt private key failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// public_jwk is pre-rendered ONCE here, at generation time, and
		// stored verbatim -- the JWKS endpoint (httpapi/oidcdiscovery.go)
		// republishes it byte-for-byte on every request, with zero
		// cryptographic work of its own (migrations/
		// 000092_oidc_signing_keys.up.sql's own doc comment on why).
		publicJWKBytes, err := json.Marshal(oidcsigning.PublicJWK(&priv.PublicKey, kid))
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: marshal public jwk failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		nowInstant := time.Now()

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: begin tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		txKeys := keys.WithTx(tx)

		// ONE read, not two: Rotate itself is the single source of truth
		// for what actually got retired, and returns that row directly
		// (RetireOIDCSigningKey's own RETURNING row) rather than making
		// this handler take a second, independently-timed GetActive read
		// of its own to learn the same thing -- see Rotate's own doc
		// comment for the concurrent-rotation forensic-falsification race
		// this closes structurally (an earlier version of this handler's
		// own "a race... is impossible" comment was itself the defect: it
		// asserted a guarantee pool.Begin's empty TxOptions/READ COMMITTED
		// isolation does not actually provide).
		created, retiredKey, hadPreviousActive, err := txKeys.Rotate(ctx, nowInstant, kid, privEncrypted, publicJWKBytes)
		if err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: rotate failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		detail := map[string]any{"new_kid": created.Kid}
		if hadPreviousActive {
			detail["retired_kid"] = retiredKey.Kid
		}
		// audit_log records the rotation itself (a platform-wide security
		// posture change, §13.3) -- written in the SAME transaction as
		// the change, never a best-effort side write.
		if err := recordAuditLog(ctx, auditLog.WithTx(tx), actorUserID, "cloud_identity_signing_key.rotated", "oidc_signing_key", created.Kid, detail); err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: record audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: rotate cloud identity signing key: commit tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := restdtos.RotateCloudIdentitySigningKeyResponse{
			ActiveKid:       created.Kid,
			ActiveCreatedAt: created.CreatedAt.Time,
		}
		if hadPreviousActive {
			// RetiredAt comes from retiredKey's own real, committed
			// retired_at column -- not the nowInstant Go-side variable
			// passed INTO Rotate -- so this response reports what was
			// actually persisted, not merely what was requested.
			retiredKid := retiredKey.Kid
			retiredAt := retiredKey.RetiredAt.Time
			publishableUntil := retiredAt.Add(timeouts.CloudIdentitySigningKeyOverlapWindow)
			resp.RetiredKid = &retiredKid
			resp.RetiredAt = &retiredAt
			resp.PublishableUntil = &publishableUntil
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
