package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// WebhookDeliveryStore is a thin, pass-through wrapper around the
// sqlc-generated webhook_deliveries queries (§5.1's dedupe/coalescing
// claim, §4.3). No caching, no retries, no business rules -- the
// concrete GitHub/Slack/Linear webhook endpoints that actually call
// Claim land in Steps 32/33/34.
type WebhookDeliveryStore struct {
	q *sqlcgen.Queries
}

// NewWebhookDeliveryStore builds a WebhookDeliveryStore backed by pool.
func NewWebhookDeliveryStore(pool *pgxpool.Pool) *WebhookDeliveryStore {
	return &WebhookDeliveryStore{q: sqlcgen.New(pool)}
}

// WithTx returns a WebhookDeliveryStore whose queries run on tx instead
// of the pool this store was built with -- mirrors EventStore/
// OutboxStore's own WithTx convention exactly, ready for a future caller
// that needs to claim a delivery in the SAME transaction as whatever
// session/event work that delivery triggers.
func (s *WebhookDeliveryStore) WithTx(tx pgx.Tx) *WebhookDeliveryStore {
	return &WebhookDeliveryStore{q: s.q.WithTx(tx)}
}

// Claim attempts an atomic first-writer-wins claim on (provider,
// deliveryID) -- see ClaimWebhookDelivery's own doc comment
// (postgres/queries/webhookdeliveries.sql) for the full "(xmax = 0) AS
// inserted" reasoning. Row.Inserted reports whether THIS call actually
// inserted a fresh row (true -- process this delivery) or found an
// already-claimed one from an earlier delivery of the same identity
// (false -- a genuine redelivery/duplicate, skip processing).
func (s *WebhookDeliveryStore) Claim(ctx context.Context, provider, deliveryID string) (sqlcgen.ClaimWebhookDeliveryRow, error) {
	return s.q.ClaimWebhookDelivery(ctx, sqlcgen.ClaimWebhookDeliveryParams{
		Provider:   provider,
		DeliveryID: deliveryID,
	})
}

// Release un-claims a (provider, deliveryID) this same caller previously
// claimed via Claim but then failed to actually process -- see
// ReleaseWebhookDelivery's own doc comment (postgres/queries/
// webhookdeliveries.sql) for why this exists: without it, a claim that
// wins but is never followed by successful processing would silently and
// permanently swallow every future redelivery of that same id.
func (s *WebhookDeliveryStore) Release(ctx context.Context, provider, deliveryID string) error {
	return s.q.ReleaseWebhookDelivery(ctx, sqlcgen.ReleaseWebhookDeliveryParams{
		Provider:   provider,
		DeliveryID: deliveryID,
	})
}
