package chatgptlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/chatgptoauth"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/platform"
)

// Deps bundles every store/config StartLink/PollLink/Unlink need — mirrors
// this codebase's own established "Deps struct, not a long positional
// parameter list" precedent (internal/app/identitylink.Deps et al).
type Deps struct {
	Pool                *pgxpool.Pool
	LinkAttempts        *postgres.ChatGPTLinkAttemptStore
	ProviderCredentials *postgres.ProviderCredentialStore
	AuditLog            *postgres.AuditLogStore
	DeviceFlow          *chatgptoauth.Client
	TokenEncryptionKey  []byte
	Timeouts            platform.Timeouts
}

// The four Status.Status values — mirrors restdtos.ChatGPTLinkStatusStatus
// 's own enum verbatim (contracts/rest/v1/dtos.schema.json's
// $defs.ChatGPTLinkStatus).
const (
	StatusUnlinked    = "unlinked"
	StatusPending     = "pending"
	StatusLinked      = "linked"
	StatusNeedsRelink = "needs_relink"
)

// Status is this package's own domain-side mirror of restdtos.
// ChatGPTLinkStatus — the httpapi handler converts 1:1, never re-deriving
// anything.
type Status struct {
	Status          string
	VerificationURL string
	UserCode        string
	ExpiresAt       *time.Time
}

// oauthCredentialBlob mirrors httpapi's own identically-named type
// (providercredentialsdelivery.go) byte-for-byte — the same {access,
// refresh, expires_ms, account_id} JSON document §29.4 specifies for an
// oauth-kind row's value_encrypted column. Independently declared here
// (not imported — httpapi's own type is unexported, and importing
// internal/adapters/inbound/httpapi from this app-layer package would
// invert this codebase's own dependency direction) but must stay
// byte-for-byte identical; both are exercised against the SAME real
// Postgres instance in this Step's own integration tests.
type oauthCredentialBlob struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	ExpiresMs int64  `json:"expires_ms"`
	AccountID string `json:"account_id"`
}

// StartLink begins a device-flow link attempt for userID — POST
// /api/me/chatgpt-link (§29.3 step 1). Reuses a still-unexpired existing
// attempt rather than minting a duplicate device code on every click,
// mirroring internal/app/identitylink's own createOrReuseLinkPrompt
// precedent exactly.
func StartLink(ctx context.Context, deps Deps, userID pgtype.UUID) (Status, error) {
	latest, err := deps.LinkAttempts.GetLatestForUser(ctx, userID)
	switch {
	case err == nil:
		if latest.ExpiresAt.Valid && latest.ExpiresAt.Time.After(time.Now()) {
			return attemptToStatus(latest), nil
		}
		// Expired -- fall through and mint a fresh one. Best-effort
		// cleanup of the stale row; its own failure must never block a
		// fresh link attempt (mirrors identitylink.Consume's identical
		// "best-effort cleanup, never masks the real outcome" posture).
		_ = deps.LinkAttempts.Delete(ctx, latest.ID)
	case errors.Is(err, pgx.ErrNoRows):
		// No attempt at all yet -- proceed to mint one.
	default:
		return Status{}, fmt.Errorf("chatgptlink: look up latest attempt: %w", err)
	}

	started, err := deps.DeviceFlow.StartDeviceAuth(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("chatgptlink: start device auth: %w", err)
	}

	// started.ExpiresAt is auth.openai.com's own real, server-provided
	// expiry for this device code (live-verified by this package's own
	// usercode canary, chatgptoauth's own doc comment) -- authoritative,
	// used directly rather than any Narvi-side invented duration. Capped
	// defensively at ChatGPTLinkAttemptTTL from now: this device code is
	// meaningless to Narvi past the point its own Settings-page prompt
	// would be considered stale regardless of what the server claims.
	expiresAt := started.ExpiresAt
	if cap := time.Now().Add(deps.Timeouts.ChatGPTLinkAttemptTTL); cap.Before(expiresAt) {
		expiresAt = cap
	}
	created, err := deps.LinkAttempts.Create(ctx, userID, started.DeviceAuthID, started.UserCode, int32(started.Interval/time.Second), expiresAt)
	if err != nil {
		return Status{}, fmt.Errorf("chatgptlink: create link attempt: %w", err)
	}
	return attemptToStatus(created), nil
}

// PollLink advances userID's own current link attempt by AT MOST one
// upstream call — GET /api/me/chatgpt-link (§29.3 step 2), called by the
// Settings page's own poll loop. Never blocks waiting for a grant; always
// returns the CURRENT state immediately.
//
// Five outcomes, in order:
//  1. No attempt row at all -> report whatever provider_credentials
//     already says (unlinked/linked/needs_relink).
//  2. An attempt row exists but has expired -> best-effort delete, then
//     same as (1).
//  3. An attempt row exists, live, but was polled too recently (server-
//     provided interval not yet elapsed) -> report "pending" WITHOUT
//     calling upstream at all (§29.3 point 2's own throttle).
//  4. An attempt row exists, live, interval elapsed, upstream still says
//     pending (ErrDeviceAuthPending) -> record the poll attempt, report
//     "pending".
//  5. An attempt row exists, live, interval elapsed, upstream GRANTS ->
//     exchange the code, encrypt+store the token pair, delete every
//     pending attempt for this user, audit-log the link, report "linked".
func PollLink(ctx context.Context, deps Deps, userID pgtype.UUID) (Status, error) {
	attempt, err := deps.LinkAttempts.GetLatestForUser(ctx, userID)
	switch {
	case err == nil:
		// handled below
	case errors.Is(err, pgx.ErrNoRows):
		return currentLinkedStatus(ctx, deps, userID)
	default:
		return Status{}, fmt.Errorf("chatgptlink: look up latest attempt: %w", err)
	}

	if !attempt.ExpiresAt.Valid || !attempt.ExpiresAt.Time.After(time.Now()) {
		_ = deps.LinkAttempts.Delete(ctx, attempt.ID)
		return currentLinkedStatus(ctx, deps, userID)
	}

	interval := time.Duration(attempt.IntervalSeconds) * time.Second
	if attempt.LastPolledAt.Valid && time.Since(attempt.LastPolledAt.Time) < interval {
		return attemptToStatus(attempt), nil
	}

	// Record the poll attempt BEFORE actually polling upstream (see
	// ChatGPTLinkAttemptStore.UpdateLastPolledAt's own doc comment: a
	// crash mid-poll must still count against the throttle, never allow
	// an immediate retry storm).
	polled, err := deps.LinkAttempts.UpdateLastPolledAt(ctx, attempt.ID, time.Now())
	if err != nil {
		return Status{}, fmt.Errorf("chatgptlink: update last polled at: %w", err)
	}

	granted, err := deps.DeviceFlow.PollDeviceToken(ctx, attempt.DeviceAuthID, attempt.UserCode)
	switch {
	case errors.Is(err, chatgptoauth.ErrDeviceAuthPending):
		return attemptToStatus(polled), nil
	case err != nil:
		// A real upstream failure (not "still pending") -- logged and
		// degraded to "still pending" for THIS call rather than failing
		// the whole poll: the human is mid-flow, watching the Settings
		// page; a single transient hiccup must not read as "link failed"
		// when the attempt itself is still live and may succeed on the
		// next poll.
		platform.Logger(ctx).Warn("chatgptlink: poll device token failed", "error", err)
		return attemptToStatus(polled), nil
	}

	tokens, err := deps.DeviceFlow.ExchangeAuthorizationCode(ctx, granted.AuthorizationCode, granted.CodeVerifier)
	if err != nil {
		// Unlike the pending-poll loop above, a failure HERE follows a
		// GRANT the human already approved -- a genuinely exceptional,
		// almost-certainly-non-retryable outcome (the authorization code
		// itself is normally single-use), surfaced as a real error rather
		// than silently degraded to "still pending". The attempt row is
		// left in place; a subsequent StartLink call (a fresh "Connect"
		// click) mints a brand new device code once this one's own
		// expiry passes, rather than this call attempting any retry
		// machinery of its own.
		return Status{}, fmt.Errorf("chatgptlink: exchange authorization code: %w", err)
	}

	if err := storeLinkedCredential(ctx, deps, userID, tokens); err != nil {
		return Status{}, err
	}
	return Status{Status: StatusLinked}, nil
}

// Unlink removes userID's own linked ChatGPT account (if any) — DELETE
// /api/me/chatgpt-link (§29.3: "unlink deletes it"). Not an error to call
// on an already-unlinked user (idempotent, matching every other delete
// endpoint's own convention in this codebase).
func Unlink(ctx context.Context, deps Deps, userID pgtype.UUID) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("chatgptlink: begin unlink transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	deleted, err := deps.ProviderCredentials.WithTx(tx).DeleteOAuthForUser(ctx, userID.String(), sqlcgen.ProviderCredentialProviderOpenai)
	if err != nil {
		return fmt.Errorf("chatgptlink: delete oauth credential: %w", err)
	}
	if err := deps.LinkAttempts.WithTx(tx).DeleteForUser(ctx, userID); err != nil {
		return fmt.Errorf("chatgptlink: delete pending link attempts: %w", err)
	}
	if deleted > 0 {
		if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), userID, "chatgpt_account.unlinked", "provider_credential", userID.String(), nil); err != nil {
			return fmt.Errorf("chatgptlink: record unlink audit log: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("chatgptlink: commit unlink transaction: %w", err)
	}
	return nil
}

// storeLinkedCredential is PollLink's own grant-success tail: encrypt the
// {access, refresh, expires_ms, account_id} blob, upsert it as this
// user's own scope=user/kind=oauth provider_credentials row, delete every
// pending attempt for this user, and audit-log the link -- all in ONE
// transaction, so a crash between steps can never leave a linked-but-
// still-pending or pending-but-secretly-linked inconsistent state.
func storeLinkedCredential(ctx context.Context, deps Deps, userID pgtype.UUID, tokens chatgptoauth.TokenResult) error {
	// Unlike a refresh (internal/app/chatgptrefresh, which per §29.10 risk
	// 7 must PRESERVE the already-stored accountId rather than trust
	// whatever a refresh response's own id_token says, chatgptoauth.
	// Client's own doc comment), a BRAND NEW link has no prior stored
	// value to fall back on -- an empty AccountID here (chatgptoauth's own
	// best-effort id_token parse failed) is a real, reportable error, not
	// a silently-degraded link.
	if tokens.AccountID == "" {
		return fmt.Errorf("chatgptlink: exchange response carried no chatgpt_account_id claim")
	}

	blob, err := json.Marshal(oauthCredentialBlob{
		Access:    tokens.AccessToken,
		Refresh:   tokens.RefreshToken,
		ExpiresMs: time.Now().Add(tokens.ExpiresIn).UnixMilli(),
		AccountID: tokens.AccountID,
	})
	if err != nil {
		return fmt.Errorf("chatgptlink: marshal oauth credential blob: %w", err)
	}
	encrypted, err := platform.EncryptToken(deps.TokenEncryptionKey, blob)
	if err != nil {
		return fmt.Errorf("chatgptlink: encrypt oauth credential blob: %w", err)
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("chatgptlink: begin link transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := deps.ProviderCredentials.WithTx(tx).UpsertOAuth(ctx, userID.String(), sqlcgen.ProviderCredentialProviderOpenai, encrypted, time.Now().Add(tokens.ExpiresIn)); err != nil {
		return fmt.Errorf("chatgptlink: upsert oauth credential: %w", err)
	}
	if err := deps.LinkAttempts.WithTx(tx).DeleteForUser(ctx, userID); err != nil {
		return fmt.Errorf("chatgptlink: delete pending link attempts: %w", err)
	}
	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), userID, "chatgpt_account.linked", "provider_credential", userID.String(), nil); err != nil {
		return fmt.Errorf("chatgptlink: record link audit log: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("chatgptlink: commit link transaction: %w", err)
	}
	return nil
}

// currentLinkedStatus reports whatever provider_credentials already knows
// for userID+openai, with no pending attempt in the picture at all --
// PollLink/StartLink's own shared "nothing in flight" branch.
func currentLinkedStatus(ctx context.Context, deps Deps, userID pgtype.UUID) (Status, error) {
	row, err := deps.ProviderCredentials.GetOAuthForUser(ctx, userID.String(), sqlcgen.ProviderCredentialProviderOpenai)
	switch {
	case err == nil:
		if row.OauthNeedsRelink {
			return Status{Status: StatusNeedsRelink}, nil
		}
		return Status{Status: StatusLinked}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return Status{Status: StatusUnlinked}, nil
	default:
		return Status{}, fmt.Errorf("chatgptlink: get oauth credential: %w", err)
	}
}

// attemptToStatus renders a live chatgpt_link_attempts row as a "pending"
// Status.
func attemptToStatus(attempt sqlcgen.ChatgptLinkAttempt) Status {
	expiresAt := attempt.ExpiresAt.Time
	return Status{
		Status:          StatusPending,
		VerificationURL: chatgptoauth.VerificationURL,
		UserCode:        attempt.UserCode,
		ExpiresAt:       &expiresAt,
	}
}
