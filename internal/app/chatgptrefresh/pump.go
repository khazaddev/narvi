package chatgptrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
// users, PumpOnce's own single-transaction-per-batch shape (see package
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
// alongside store, for PumpOnce's own per-batch transaction -- mirrors
// outboxworker.NewBuilder's identical reasoning), deviceFlow (the same
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

// PumpOnce runs exactly one pump tick, entirely inside ONE transaction
// (see package doc.go for why this deliberately differs from
// outboxworker's own claim-then-release shape): claims up to
// pumpBatchSize oauth-kind provider_credentials rows expiring within
// platform.Timeouts.ChatGPTOAuthRefreshMargin of now (FOR UPDATE SKIP
// LOCKED, so a concurrent tick -- this pod's next one, or another pod's
// -- claims a disjoint batch), then refreshes each in turn, still holding
// every claimed row's own lock, before committing the whole batch at
// once. Exported (rather than only reachable through Run's own loop) so
// tests can drive exactly one tick deterministically, matching
// outboxworker.Builder.PumpOnce's own precedent.
func (p *Pump) PumpOnce(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("chatgptrefresh: begin batch transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := p.store.WithTx(tx)

	expiring, err := txStore.ListExpiringOAuth(ctx, time.Now().Add(p.timeouts.ChatGPTOAuthRefreshMargin), pumpBatchSize)
	if err != nil {
		return fmt.Errorf("chatgptrefresh: list expiring oauth credentials: %w", err)
	}

	for _, row := range expiring {
		p.refreshOne(ctx, txStore, row)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("chatgptrefresh: commit batch transaction: %w", err)
	}
	return nil
}

// refreshOne refreshes ONE already-claimed row -- decrypt, call POST
// /oauth/token (grant_type=refresh_token), and either rewrite the row
// (success) or mark it oauth_needs_relink (a terminal failure, §29.5) or
// leave it untouched (a transient failure, retried automatically next
// pump cycle since nothing about the row changed). Every outcome is
// logged; none is ever returned as an error to PumpOnce -- a single
// row's own failure must never abort or roll back the whole batch's
// transaction (a sibling row's own successful refresh must still commit).
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
