// This file (digestslacknotifier.go) implements §21's own
// (§21.3) Slack digest notifier: one small ports.Notifier handling
// ports.NotificationKindSlackDigest rows -- a plain chat.postMessage of
// internal/domain/digest.Render's own already-rendered, deterministic
// text, mirroring planSlackNotifier's own "one small wrapper type per
// notification family" precedent exactly, but simpler: no Block Kit, no
// persisted message ref (a digest is fire-and-forget, unlike a plan
// approval message nothing ever needs to chat.update later).

package outboxworker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/narvidev/narvi/internal/adapters/outbound/slackapi"
	"github.com/narvidev/narvi/internal/app/ports"
)

// digestSlackNotifier implements ports.Notifier for
// ports.NotificationKindSlackDigest rows.
type digestSlackNotifier struct {
	client *slackapi.Client
}

var _ ports.Notifier = (*digestSlackNotifier)(nil)

// NewDigestSlackNotifier builds a ports.Notifier for
// ports.NotificationKindSlackDigest rows, backed by client -- the SAME
// *slackapi.Client already constructed for every other Slack notifier
// kind, no separate bot token/client needed.
func NewDigestSlackNotifier(client *slackapi.Client) ports.Notifier {
	return &digestSlackNotifier{client: client}
}

func (n *digestSlackNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	if notification.Kind != ports.NotificationKindSlackDigest {
		return fmt.Errorf("outboxworker: digestSlackNotifier: unrecognized notification kind %q", notification.Kind)
	}

	var payload slackapi.DigestPayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("outboxworker: digestSlackNotifier: unmarshal payload: %w", err)
	}

	_, _, err := n.client.PostMessage(ctx, payload.ChannelID, payload.Text)
	return err
}
