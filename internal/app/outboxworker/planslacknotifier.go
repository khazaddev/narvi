// This file (planslacknotifier.go) implements Step 38's ("plan mode,
// cross-channel", §8.1/§13.3) own Slack plan-mode notifier: one small
// ports.Notifier implementation handling BOTH
// ports.NotificationKindSlackPlanApproval (post the real interactive
// Block Kit approval-request message, then persist its own channel+ts onto
// the plans row) and ports.NotificationKindSlackPlanDecided (chat.update
// an existing message to reflect a rendered decision) -- mirroring
// linearNotifier's own precedent of one small wrapper type per
// provider-specific notification family, constructed once in
// cmd/control-plane/main.go and registered under BOTH kinds in the same
// kind->Notifier routing map (a single Go value may back multiple map
// keys; outboxworker.Builder.attempt routes purely by row.Kind, so this is
// no different from registering two structurally-identical rows).

package outboxworker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// planSlackNotifier implements ports.Notifier for both
// ports.NotificationKindSlackPlanApproval and
// ports.NotificationKindSlackPlanDecided rows.
type planSlackNotifier struct {
	client *slackapi.Client
	plans  *postgres.PlanStore
}

// var _ ports.Notifier = (*planSlackNotifier)(nil) makes a Notifier
// signature drift a build error, not a runtime surprise.
var _ ports.Notifier = (*planSlackNotifier)(nil)

// NewPlanSlackNotifier builds a ports.Notifier for
// ports.NotificationKindSlackPlanApproval/ports.NotificationKindSlackPlanDecided
// rows, backed by client (the SAME *slackapi.Client already constructed for
// the plain ports.NotificationKindSlack notifier -- no separate bot
// token/client needed) and plans (so a successful approval-request post can
// persist its own channel+ts back onto the plan row). Called once by
// cmd/control-plane/main.go's own kind->Notifier map assembly, mirroring
// NewLinearNotifier's own precedent exactly.
func NewPlanSlackNotifier(client *slackapi.Client, plans *postgres.PlanStore) ports.Notifier {
	return &planSlackNotifier{client: client, plans: plans}
}

// Deliver implements ports.Notifier, dispatching on notification.Kind --
// the delivery worker only ever routes a row to this notifier under one of
// the two kinds it was registered for (see this file's own top doc
// comment), so an unrecognized third kind here is defensive, not expected.
func (n *planSlackNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	switch notification.Kind {
	case ports.NotificationKindSlackPlanApproval:
		return n.deliverApproval(ctx, notification.Payload)
	case ports.NotificationKindSlackPlanDecided:
		return n.deliverDecided(ctx, notification.Payload)
	default:
		return fmt.Errorf("outboxworker: planSlackNotifier: unrecognized notification kind %q", notification.Kind)
	}
}

// deliverApproval posts the real interactive plan-approval-request message
// and, on success, persists its own real channel+ts onto the plans row
// (PlanStore.SetSlackMessageRef) -- the ONE place this ever gets written,
// so a later decision (from any entry point) can chat.update this exact
// message. A failure to persist the ref is itself returned as this
// Deliver call's own error (not swallowed): without the ref stored, no
// future decision could ever update this message, silently breaking the
// cross-channel notify guarantee this whole Step exists to provide -- far
// better to retry the WHOLE delivery (idempotent from Slack's own
// perspective: a retried post creates a second message, an accepted,
// honestly-documented tradeoff of "correctness over avoiding a rare
// duplicate post", matching this codebase's own outbox-retry precedent for
// every other notifier) than to silently leave the ref unset.
func (n *planSlackNotifier) deliverApproval(ctx context.Context, raw json.RawMessage) error {
	var payload slackapi.PlanApprovalPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("outboxworker: decode slack plan-approval payload: %w", err)
	}

	channel, ts, err := n.client.PostPlanApprovalMessage(ctx, payload)
	if err != nil {
		return err
	}

	var planID pgtype.UUID
	if err := planID.Scan(payload.PlanID); err != nil {
		return fmt.Errorf("outboxworker: parse plan id %q from slack plan-approval payload: %w", payload.PlanID, err)
	}
	if err := n.plans.SetSlackMessageRef(ctx, planID, channel, ts); err != nil {
		return fmt.Errorf("outboxworker: persist slack message ref for plan %s: %w", payload.PlanID, err)
	}
	return nil
}

// deliverDecided calls chat.update against the already-known channel+ts
// (carried in the payload itself -- no DB lookup needed here, unlike
// deliverApproval above) to reflect a plan's final decided outcome.
func (n *planSlackNotifier) deliverDecided(ctx context.Context, raw json.RawMessage) error {
	var payload slackapi.PlanDecidedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("outboxworker: decode slack plan-decided payload: %w", err)
	}
	return n.client.UpdateMessage(ctx, payload.ChannelID, payload.MessageTS, payload.Text)
}
