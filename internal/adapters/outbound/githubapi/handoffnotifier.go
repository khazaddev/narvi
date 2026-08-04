package githubapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// HandoffPayload is the JSON shape internal/app/outboxworker expects to
// find in an outbox entry's own payload column for a
// ports.NotificationKindHandoffSentinel row -- enqueued by internal/app/
// sessionactor/handoffsentinel.go (Step 49, "handoff-readiness sentinel",
// §14.4) once a scoped-session PR's own sentinel run has something to
// report. Owner/Repo/PRNumber are the PR's own identity; Body is the
// ALREADY-RENDERED comment (internal/domain/handoff.RenderComment's own
// result -- this package never renders anything itself, only delivers
// already-rendered text, mirroring VerdictPayload's own identical
// discipline); Label is the fixed handoff.Label constant, carried here
// rather than hard-coded a second time in this package so a future
// rename of that constant has exactly one place to change.
type HandoffPayload struct {
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	PRNumber int    `json:"pr_number"`
	Body     string `json:"body"`
	Label    string `json:"label"`
}

// HandoffNotifier implements ports.Notifier for
// ports.NotificationKindHandoffSentinel: ONE Deliver call posts the
// sentinel's summary as a plain issue comment AND adds the "handoff"
// label -- both parts of the SAME sentinel run, delivered together so a
// caller enqueues exactly one outbox row, never two independently-retried
// halves (mirrors VerdictNotifier's own identical "one Deliver, two
// GitHub calls" shape).
type HandoffNotifier struct {
	adapter  *Adapter
	botToken string
}

var _ ports.Notifier = (*HandoffNotifier)(nil)

// NewHandoffNotifier builds a HandoffNotifier wrapping adapter,
// authenticating every call with botToken -- this sentinel's own comment
// and label are a system-generated notice, never attributed to any
// individual reviewer or the session's own creator, mirroring
// NewVerdictNotifier's own identical "single, statically-configured bot
// credential" choice.
func NewHandoffNotifier(adapter *Adapter, botToken string) *HandoffNotifier {
	return &HandoffNotifier{adapter: adapter, botToken: botToken}
}

// Deliver implements ports.Notifier: decodes n.Payload as HandoffPayload,
// adds the label, then posts the comment. n.Kind is not checked -- mirrors
// VerdictNotifier.Deliver's own identical "only ever asked to Deliver its
// own matching Kind in practice" precedent.
//
// Ordering (label FIRST, comment LAST) is deliberate -- and the reverse of
// what an earlier version of this function did, which had a real bug: with
// comment-then-label, a transient failure on the label call AFTER a
// successful comment left the outbox row "pending", so the next retry
// re-ran Deliver from the top and posted a SECOND comment. AddLabels is
// naturally idempotent on GitHub's own side (adding a label a PR already
// carries is a harmless no-op); PostIssueComment is NOT. Putting the
// non-idempotent operation LAST means: once it succeeds, Deliver returns
// nil and the row is never retried again; a retry after any EARLIER
// failure re-runs AddLabels (safe no-op) and then attempts
// PostIssueComment again -- which, never having succeeded on a prior
// attempt, posts exactly once. This is exactly why the caller
// (internal/app/sessionactor/handoffsentinel.go) ALSO claims idempotency
// before ever enqueueing this outbox row at all -- Deliver itself is
// never asked to re-run for a PR that already succeeded, only for a
// genuine transient-failure retry of the SAME still-pending attempt; this
// ordering is what makes that retry safe.
func (n *HandoffNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	var payload HandoffPayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("githubapi: decode handoff payload: %w", err)
	}

	if payload.Label != "" {
		if err := n.adapter.AddLabels(ctx, payload.Owner, payload.Repo, payload.PRNumber, n.botToken, []string{payload.Label}); err != nil {
			return fmt.Errorf("githubapi: deliver handoff (add label): %w", err)
		}
	}

	if err := n.adapter.PostIssueComment(ctx, payload.Owner, payload.Repo, payload.PRNumber, n.botToken, payload.Body); err != nil {
		return fmt.Errorf("githubapi: deliver handoff (post comment): %w", err)
	}

	return nil
}
