package outboxworker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// linearNotifier implements ports.Notifier for ports.NotificationKindLinear
// rows -- see this package's own doc.go for why the real Linear API
// credential lookup (LinearInstallationStore.GetByOrganizationID +
// platform.DecryptToken) lives HERE, in app/outboxworker, rather than
// inside internal/adapters/outbound/linearapi itself: a hard security
// requirement (a decrypted token must never sit in the outbox payload at
// rest, only an organization_id reference -- decrypt fresh at delivery
// time) that also happens to keep linearapi free of a postgres-store
// dependency no other outbound adapter package carries.
type linearNotifier struct {
	client             *linearapi.Client
	installations      *postgres.LinearInstallationStore
	tokenEncryptionKey []byte
}

// var _ ports.Notifier = (*linearNotifier)(nil) makes a Notifier signature
// drift a build error, not a runtime surprise.
var _ ports.Notifier = (*linearNotifier)(nil)

// NewLinearNotifier builds a ports.Notifier for ports.NotificationKindLinear
// rows, backed by client/installations/tokenEncryptionKey -- called once
// by cmd/control-plane/main.go's own kind->Notifier map assembly,
// mirroring how internal/adapters/outbound/{slackapi,githubapi}'s own
// constructors are each called exactly once there too. The concrete
// *linearNotifier type stays unexported -- callers depend on the
// ports.Notifier interface it returns, never the type itself.
func NewLinearNotifier(client *linearapi.Client, installations *postgres.LinearInstallationStore, tokenEncryptionKey []byte) ports.Notifier {
	return &linearNotifier{
		client:             client,
		installations:      installations,
		tokenEncryptionKey: tokenEncryptionKey,
	}
}

// Deliver implements ports.Notifier: decodes n.Payload as linearapi.
// Payload, looks up the target workspace's own stored linear_installations
// row by OrganizationID, decrypts its access token FRESH (never cached,
// never persisted anywhere outside that one already-encrypted-at-rest
// row), and posts the outcome-shaped AgentActivity Payload.Success selects
// (CreateResponseActivity vs CreateErrorActivity). A pgx.ErrNoRows result
// from the installation lookup (no admin has connected this workspace) is
// a real delivery failure like any other -- returned as an error so the
// caller's own backoff/dead-letter machinery handles it identically to a
// real network failure, never silently swallowed.
func (n *linearNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	var payload linearapi.Payload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("outboxworker: decode linear payload: %w", err)
	}

	installation, err := n.installations.GetByOrganizationID(ctx, payload.OrganizationID)
	if err != nil {
		return fmt.Errorf("outboxworker: get linear installation for organization %q: %w", payload.OrganizationID, err)
	}

	accessToken, err := platform.DecryptToken(n.tokenEncryptionKey, installation.AccessTokenEncrypted)
	if err != nil {
		// The decrypt error itself is safe to log (see platform.
		// DecryptToken's own doc comment: it never contains the ciphertext
		// or plaintext) -- the plaintext token it would have produced is
		// NEVER logged, here or anywhere else.
		return fmt.Errorf("outboxworker: decrypt linear access token: %w", err)
	}

	if payload.Success {
		return n.client.CreateResponseActivity(ctx, string(accessToken), payload.AgentSessionID, payload.Text)
	}
	return n.client.CreateErrorActivity(ctx, string(accessToken), payload.AgentSessionID, payload.Text)
}
