package chatgptrefresh

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
	"github.com/khazaddev/narvi/internal/platform"
)

// pumpBatchSize bounds how many oauth credentials one PumpOnce call
// refreshes -- mirrors outboxworker's own identically-named, identically-
// valued constant (a plain Go const, not a platform.Timeouts field, since
// it bounds a COUNT, not a duration). Realistic deployments expect a low
// single-digit number of linked accounts due for refresh in any given 6h
// window (§29.5's own 72h margin against a verified 10-day access
// lifetime); if this pump ever needs to scale to thousands of linked
// users, PumpOnce's own bounded-iterations-per-tick shape (see package
// doc.go) would need revisiting first.
const pumpBatchSize = 20

// oauthCredentialBlob mirrors httpapi's and chatgptlink's own identically-
// named type byte-for-byte (§29.4's {access, refresh, expires_ms,
// account_id} document) -- independently declared here for the same
// "importing a sibling app/inbound-adapter package would invert this
// codebase's own dependency direction" reason chatgptlink's own copy
// documents.
type oauthCredentialBlob struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	ExpiresMs int64  `json:"expires_ms"`
	AccountID string `json:"account_id"`
}

// Pump is the refresh pump itself -- see package doc.go for the full
// design and the deliberate outboxworker-shape deviation.
type Pump struct {
	store              *postgres.ProviderCredentialStore
	pool               *pgxpool.Pool
	deviceFlow         *chatgptoauth.Client
	tokenEncryptionKey []byte
	timeouts           platform.Timeouts
}

// NewPump builds a Pump backed by store/pool (pool is needed directly,
// alongside store, for refreshClaimedRow's own per-row transaction --
// mirrors outboxworker.NewBuilder's identical reasoning), deviceFlow (the same
// adapter internal/app/chatgptlink uses for the link flow's own token
// calls), tokenEncryptionKey (platform.Config.TokenEncryptionKey -- the
// ONE key that ever decrypts/encrypts a provider_credentials row's own
// value, matching providercredentialsdelivery.go's identical precedent),
// and timeouts.
func NewPump(store *postgres.ProviderCredentialStore, pool *pgxpool.Pool, deviceFlow *chatgptoauth.Client, tokenEncryptionKey []byte, timeouts platform.Timeouts) *Pump {
	return &Pump{
		store:              store,
		pool:               pool,
		deviceFlow:         deviceFlow,
		tokenEncryptionKey: tokenEncryptionKey,
		timeouts:           timeouts,
	}
}

// Run runs the process-wide refresh-pump loop until ctx is done -- mirrors
// outboxworker.Builder.Run/app/reconciler.Reconciler.Run's own identical
// ticker shape: platform.Timeouts.ChatGPTOAuthRefreshPumpInterval, calling
// PumpOnce each tick, logging (never propagating) any per-tick error so
// one bad tick never kills the whole loop. The caller starts this via its
// own errgroup.Go exactly once per process (cmd/control-plane/main.go).
func (p *Pump) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.timeouts.ChatGPTOAuthRefreshPumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.PumpOnce(ctx); err != nil {
				platform.Logger(ctx).Error("chatgptrefresh: tick failed", "error", err)
			}
		}
	}
}

// PumpOnce runs exactly one pump tick, in two phases -- S1's own fix
// (adversarial review) to the previous single-shared-transaction-per-
// BATCH shape (see package doc.go for the full writeup of why that shape
// was unsafe):
//
//  1. listCandidates takes a short, read-only SNAPSHOT of up to
//     pumpBatchSize due row ids, committed immediately -- so this step's
//     own FOR UPDATE SKIP LOCKED locks are released the instant it
//     returns, well before any real refresh work begins.
//  2. Each candidate id is then re-claimed and refreshed ONE AT A TIME,
//     each entirely inside its OWN short transaction, committed
//     immediately after (refreshClaimedRow below).
//
// Taking the snapshot up front (rather than re-listing fresh before each
// per-row claim) is what keeps this tick's own retry behavior matching
// the OLD design's: EACH distinct due row gets AT MOST ONE refresh
// attempt this tick, even if that attempt fails transiently and the row
// is therefore STILL due moments later -- re-listing fresh each time
// would keep re-selecting that SAME still-due row every iteration
// (nothing about it changed) instead of giving every OTHER due row in
// the batch its own chance this tick.
//
// A failure in the snapshot step itself aborts the tick and returns the
// error (Run logs it), mirroring outboxworker.Builder.PumpOnce's own
// identical split between "a batch-level failure aborts the tick" and
// "one row's own failure is isolated, logged, never propagated" -- once
// the snapshot succeeds, every per-row claim/refresh failure below is
// isolated by refreshClaimedRow. Exported (rather than only reachable
// through Run's own loop) so tests can drive exactly one tick
// deterministically, matching outboxworker.Builder.PumpOnce's own
// precedent.
func (p *Pump) PumpOnce(ctx context.Context) error {
	candidates, err := p.listCandidates(ctx)
	if err != nil {
		return fmt.Errorf("chatgptrefresh: list expiring oauth credentials: %w", err)
	}

	for _, id := range candidates {
		p.refreshClaimedRow(ctx, id)
	}
	return nil
}

// listCandidates takes PumpOnce's own up-front snapshot: up to
// pumpBatchSize due oauth-kind row ids (soonest-expiring first), inside
// ONE short transaction committed immediately after the read -- never
// held open across any of the real per-row work refreshClaimedRow does.
func (p *Pump) listCandidates(ctx context.Context) ([]pgtype.UUID, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin snapshot transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := p.store.WithTx(tx).ListExpiringOAuth(ctx, time.Now().Add(p.timeouts.ChatGPTOAuthRefreshMargin), pumpBatchSize)
	if err != nil {
		return nil, fmt.Errorf("list expiring oauth credentials: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit snapshot transaction: %w", err)
	}

	ids := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids, nil
}

// refreshClaimedRow re-claims (GetExpiringOAuthForUpdate -- FOR UPDATE
// SKIP LOCKED, re-verifying id still matches the due criteria
// listCandidates already checked a moment ago) and refreshes id
// specifically, entirely inside ONE short transaction committed before
// this function returns. A no-op when id is no longer claimable -- either
// pgx.ErrNoRows (locked by a concurrent pump instance's own still-in-
// flight refresh, or a SIBLING id's own earlier refresh this same tick
// already changed it, e.g. an unrelated write is never expected here but
// is handled identically either way) or any other claim-step error (a
// real infrastructure problem) -- logged, never propagated: one id's own
// failure must never prevent the REST of this tick's candidates from
// getting their own attempt.
//
// S1 fix (adversarial review): the previous shape ran the ENTIRE batch
// (claim every due row, then refresh each in turn) inside one shared
// transaction, committed once at the end. Two interacting problems with
// that:
//
//  1. RefreshToken (called below, inside refreshOne) is a live upstream
//     call that ROTATES the refresh token -- OpenAI consumes the old one
//     the moment this call succeeds, whether or not this pump ever
//     commits its own local rewrite. Any interruption before the shared
//     batch's own final commit (SIGTERM/rolling deploy cancelling ctx,
//     OOM, DB failover) rolled back EVERY already-rotated row in the
//     batch at once -- the DB kept the old, now upstream-consumed
//     tokens, and the next tick would replay them straight into a
//     terminal refresh_token_reused/invalid_grant failure, forcing a
//     needless re-link. Committing per row (this function) means an
//     interruption can only ever strand the ONE row currently mid-flight,
//     never a whole batch's worth.
//  2. A failed DB statement puts THAT statement's own transaction into
//     Postgres's aborted (25P02) state, failing every later statement on
//     it, including the eventual commit. Under the old shared-transaction
//     shape this could silently fail every OTHER row's own write for the
//     rest of the batch while the loop kept calling RefreshToken (so kept
//     consuming those rows' own tokens) regardless -- tokens consumed
//     with no possible write. Since every row now gets its own,
//     independent transaction, one row's DB-level failure can never
//     reach a sibling row's transaction at all.
//
// This still preserves the FOR UPDATE SKIP LOCKED guarantee exactly:
// only the ONE row actually being refreshed is ever locked, for exactly
// the duration of its own refresh call -- mirrors outboxworker's own
// claim-then-act shape (this package's own doc.go cites it), adapted to
// keep the claim and the act in the SAME transaction (unlike outboxworker)
// specifically so the lock stays held across the live upstream call --
// still the deliberate deviation doc.go documents, just correctly scoped
// to one row instead of a whole batch.
func (p *Pump) refreshClaimedRow(ctx context.Context, id pgtype.UUID) {
	logger := platform.Logger(ctx).With("provider_credential_id", id.String())

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		logger.Error("chatgptrefresh: begin per-row transaction failed", "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := p.store.WithTx(tx)

	row, err := txStore.GetExpiringOAuthForUpdate(ctx, id, time.Now().Add(p.timeouts.ChatGPTOAuthRefreshMargin))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No longer claimable right now -- not an error, see this
			// function's own doc comment.
			return
		}
		logger.Error("chatgptrefresh: re-claim credential failed", "error", err)
		return
	}

	p.refreshOne(ctx, txStore, row)

	if err := tx.Commit(ctx); err != nil {
		logger.Error("chatgptrefresh: commit per-row transaction failed", "error", err)
	}
}

// refreshOne refreshes ONE already-claimed row -- decrypt, call POST
// /oauth/token (grant_type=refresh_token), and either rewrite the row
// (success) or mark it oauth_needs_relink (a terminal failure, §29.5) or
// leave it untouched (a transient failure, retried automatically next
// pump cycle since nothing about the row changed). Every outcome is
// logged; none is ever returned as an error to refreshClaimedRow -- txStore
// here is always scoped to THIS row's own short transaction (S1 fix, see
// refreshClaimedRow's own doc comment), so there is no longer a wider
// batch transaction any write here could abort or roll back.
func (p *Pump) refreshOne(ctx context.Context, txStore *postgres.ProviderCredentialStore, row sqlcgen.ProviderCredential) {
	logger := platform.Logger(ctx).With("provider_credential_id", row.ID.String())

	plaintext, err := platform.DecryptToken(p.tokenEncryptionKey, row.ValueEncrypted)
	if err != nil {
		// Never logs the ciphertext/plaintext -- matches
		// providercredentialsdelivery.go's own identical discipline.
		logger.Error("chatgptrefresh: decrypt credential failed", "error", err)
		return
	}
	var blob oauthCredentialBlob
	if err := json.Unmarshal(plaintext, &blob); err != nil {
		logger.Error("chatgptrefresh: parse credential blob failed", "error", err)
		return
	}

	refreshed, err := p.deviceFlow.RefreshToken(ctx, blob.Refresh)
	if err != nil {
		var tokenErr *chatgptoauth.TokenError
		if errors.As(err, &tokenErr) && tokenErr.IsTerminal() {
			logger.Warn("chatgptrefresh: terminal refresh failure, marking oauth_needs_relink", "error", err, "code", tokenErr.Code)
			if _, markErr := txStore.MarkNeedsRelink(ctx, row.ID); markErr != nil {
				logger.Error("chatgptrefresh: mark needs-relink failed", "error", markErr)
			}
			return
		}
		// Transient (§29.5's own failure taxonomy, mirroring §13.2's
		// "retryable error, not an empty identity" rule): leave the row
		// exactly as it is -- the SAME pair is still served by the
		// delivery endpoint until it either succeeds on a later pump
		// cycle or the access token itself expires.
		logger.Warn("chatgptrefresh: refresh failed (transient), keeping the last stored pair and retrying next pump cycle", "error", err)
		return
	}

	// §29.10 risk 7: preserve the STORED accountId across a refresh,
	// never trust (or even look at) refreshed.AccountID -- see
	// chatgptoauth.Client's own postToken doc comment for why a refresh
	// response's own id_token is not a reliable source for it.
	newBlob, err := json.Marshal(oauthCredentialBlob{
		Access:    refreshed.AccessToken,
		Refresh:   refreshed.RefreshToken,
		ExpiresMs: time.Now().Add(refreshed.ExpiresIn).UnixMilli(),
		AccountID: blob.AccountID,
	})
	if err != nil {
		logger.Error("chatgptrefresh: marshal refreshed credential blob failed", "error", err)
		return
	}
	encrypted, err := platform.EncryptToken(p.tokenEncryptionKey, newBlob)
	if err != nil {
		logger.Error("chatgptrefresh: encrypt refreshed credential blob failed", "error", err)
		return
	}

	if _, err := txStore.UpdateOAuthTokens(ctx, row.ID, encrypted, time.Now().Add(refreshed.ExpiresIn)); err != nil {
		logger.Error("chatgptrefresh: update refreshed credential failed", "error", err)
	}
}
