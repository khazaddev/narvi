package rwx

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// PreviewDispatchPayload is the JSON shape internal/app/outboxworker
// expects to find in an outbox entry's own payload column for a
// ports.NotificationKindRWXPreviewDispatch row — enqueued by
// internal/app/sessionactor's own createPRBestEffort (pushpr.go, §4.1.2
// point 1) once a push_complete event names a repo whose per-repo preview
// setting is present. DispatchKey travels here (rather than being looked
// up fresh at delivery time the way linearNotifier looks up a workspace
// credential) because it is NOT a secret — a repo's own admin-configured
// dispatch key, safe to persist in the outbox payload at rest, unlike a
// decrypted OAuth/API token (internal/app/outboxworker's own doc comment:
// "a decrypted token must never sit in the outbox payload at rest" — a
// rule this payload never violates, since RWX_ACCESS_TOKEN itself is
// DispatchClient's own static, constructor-baked-in credential, never
// carried in any one row's payload).
type PreviewDispatchPayload struct {
	// DispatchKey selects which RWX run definition to dispatch — the
	// repo's own per-repo setting (repo_settings.rwx_preview_dispatch_key).
	DispatchKey string `json:"dispatch_key"`
	// Ref is the pushed sha — the Dispatches API's own top-level "ref"
	// (§4.1.2 point 2: "ref = the pushed sha").
	Ref string `json:"ref"`
	// PRNumber/HeadSHA/SessionID become event.dispatch.params (§4.1.2
	// point 2: "params = {pr-number, head-sha, session-id} ... from which
	// the repo's own .rwx run definition templates its app endpoint").
	PRNumber  int    `json:"pr_number"`
	HeadSHA   string `json:"head_sha"`
	SessionID string `json:"session_id"`
}

// PreviewNotifier implements ports.Notifier for
// ports.NotificationKindRWXPreviewDispatch: ONE Deliver call POSTs to
// RWX's real Dispatches API. Constructed once (cmd/control-plane/main.go)
// wrapping a *DispatchClient with a single, statically-configured RWX
// service-account credential baked in — mirroring githubapi.BotNotifier's
// own "credential baked in at construction time, never per-row" precedent
// exactly (see PreviewDispatchPayload's own doc comment for why that is
// safe here: no field carried in the payload is itself a secret).
type PreviewNotifier struct {
	client *DispatchClient
}

// var _ ports.Notifier = (*PreviewNotifier)(nil) makes a Notifier
// signature drift a build error, not a runtime surprise.
var _ ports.Notifier = (*PreviewNotifier)(nil)

// NewPreviewNotifier builds a PreviewNotifier wrapping client.
func NewPreviewNotifier(client *DispatchClient) *PreviewNotifier {
	return &PreviewNotifier{client: client}
}

// Deliver implements ports.Notifier: decodes n.Payload as
// PreviewDispatchPayload and dispatches one RWX preview build. n.Kind is
// not checked — this PreviewNotifier is only ever asked to Deliver
// ports.NotificationKindRWXPreviewDispatch rows in practice (the delivery
// worker's own kind->Notifier routing is what guarantees that, mirroring
// every other Notifier implementation in this codebase, e.g.
// githubapi.BotNotifier). Not idempotent on redelivery — an accepted
// limitation shared with every other Notifier in this codebase (e.g.
// githubapi.ReleaseManifestNotifier's own identical doc comment): RWX's
// own content-addressed build cache (§4.1.2 point 2: "repeat dispatches
// are cheap by construction") makes a genuine retry's real-world cost
// negligible regardless.
func (n *PreviewNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	var payload PreviewDispatchPayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("rwx: decode preview dispatch payload: %w", err)
	}

	params := map[string]string{
		"pr-number":  fmt.Sprintf("%d", payload.PRNumber),
		"head-sha":   payload.HeadSHA,
		"session-id": payload.SessionID,
	}
	if _, err := n.client.Dispatch(ctx, payload.DispatchKey, payload.Ref, "", params); err != nil {
		return fmt.Errorf("rwx: deliver preview dispatch: %w", err)
	}
	return nil
}
