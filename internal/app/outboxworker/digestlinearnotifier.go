package outboxworker

import (
	"context"
	"errors"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// ErrLinearDigestNotImplemented is digestLinearNotifier.Deliver's own
// permanent failure -- errors.Is-checkable, so a future Step that adds
// real Linear digest delivery has one obvious sentinel to replace,
// rather than a bare string a caller could not distinguish from a
// transient failure.
var ErrLinearDigestNotImplemented = errors.New("outboxworker: linear digest delivery is not yet implemented -- no organization-level Linear post capability exists in this codebase (see ports.NotificationKindLinearDigest's own doc comment)")

// digestLinearNotifier implements ports.Notifier for Step 62's own
// (§21.3) ports.NotificationKindLinearDigest rows.
//
// UNLIKE every other notifier in this package, Deliver here NEVER
// actually delivers anything: it always returns a clear, typed error.
// This is deliberate, not an oversight -- see ports.
// NotificationKindLinearDigest's own doc comment (notifier.go) for the
// full "why": a digest is not a reply to any one Linear AgentSession, and
// linearapi.Client exposes only AgentSession-SCOPED activity methods
// (CreateThoughtActivity/CreateResponseActivity/CreateErrorActivity) --
// there is no "post this text somewhere visible to organizationID as a
// whole" capability anywhere in this codebase's Linear adapter today, and
// building one (a new comment/issue-creation capability, and a policy
// decision about WHERE a digest-for-an-organization should even land in
// Linear's own information architecture) is real, new adapter scope this
// Step's own brief does not authorize inventing on spec.
//
// Registering this kind anyway (rather than skipping Linear channel
// discovery entirely) is itself the deliberate choice: internal/app/
// digest's own claim-before-act guarantee (digest_send_state, SELECT ...
// FOR UPDATE SKIP LOCKED) is proven end-to-end for BOTH providers this
// way, and a real caller hitting this gap gets a clear, actionable
// failure -- routed through this codebase's OWN existing outbox retry-
// then-dead-letter path (§5.1) into the decision inbox's own admin-only
// needs_attention row -- rather than either a silent no-op (the digest
// simply never arrives, with no signal anywhere that it didn't) or a
// fabricated success.
type digestLinearNotifier struct{}

var _ ports.Notifier = (*digestLinearNotifier)(nil)

// NewDigestLinearNotifier builds a ports.Notifier for
// ports.NotificationKindLinearDigest rows -- takes no dependencies at
// all (unlike every other notifier in this package), since it never
// actually calls out to Linear.
func NewDigestLinearNotifier() ports.Notifier {
	return &digestLinearNotifier{}
}

func (n *digestLinearNotifier) Deliver(_ context.Context, notification ports.Notification) error {
	if notification.Kind != ports.NotificationKindLinearDigest {
		return fmt.Errorf("outboxworker: digestLinearNotifier: unrecognized notification kind %q", notification.Kind)
	}
	return ErrLinearDigestNotImplemented
}
