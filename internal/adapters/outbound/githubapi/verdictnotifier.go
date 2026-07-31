package githubapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// VerdictPayload is the JSON shape internal/app/outboxworker expects to
// find in an outbox entry's own payload column for a
// ports.NotificationKindGitHubVerdict row -- enqueued by internal/
// adapters/inbound/httpapi/reviewverdict.go (Step 47, "server-side
// verdict", §8.2) at verdict-posting-tool-call time. Owner/Repo/PRNumber
// are the review session's own PR identity (github_pr_sessions); Event is
// one of internal/domain/reviewpost.FormalReviewEvent's own two values
// (COMMENT/REQUEST_CHANGES, ComputeFormalReviewEvent's result -- see that
// type's own doc comment for why APPROVE is never one of them); Body is
// the ALREADY-RENDERED verdict comment (internal/domain/reviewpost.
// RenderVerdictComment's result -- this package never renders anything
// itself, only delivers already-rendered text).
//
// RiskLevel is deliberately the ONE piece of raw verdict data carried
// here rather than a precomputed add/remove label plan: label sync
// (VerdictNotifier.Deliver below) fetches the PR's own CURRENT labels via
// a real ListLabels call and computes internal/domain/reviewpost.
// ComputeLabelSync AT DELIVERY TIME, not at enqueue time -- mirroring
// this codebase's own established discipline that a real outbound network
// call happens only inside a Notifier's own Deliver, never synchronously
// inside the inbound HTTP handler that enqueues the outbox row (the SAME
// "never a real network call in the request path" precedent every other
// enqueue site in this codebase already follows -- e.g. §5.1's own outbox
// pattern discussion). This also makes the sync self-healing against
// whatever labels the PR ACTUALLY carries by the time delivery runs,
// rather than a plan computed against a possibly-stale read from
// (potentially much earlier) enqueue time.
type VerdictPayload struct {
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	PRNumber  int    `json:"pr_number"`
	Event     string `json:"event"`
	Body      string `json:"body"`
	RiskLevel string `json:"risk_level"`
}

// VerdictNotifier implements ports.Notifier for
// ports.NotificationKindGitHubVerdict: ONE Deliver call submits the
// formal pull request review (CreateReview) AND syncs the review:*-risk
// label vocabulary (ListLabels + AddLabels/RemoveLabel) -- both parts of
// posting the SAME verdict, delivered together so a caller enqueues
// exactly one outbox row per verdict-posting-tool call, never two
// independently-retried halves.
type VerdictNotifier struct {
	adapter  *Adapter
	botToken string
}

var _ ports.Notifier = (*VerdictNotifier)(nil)

// NewVerdictNotifier builds a VerdictNotifier wrapping adapter,
// authenticating every call it makes with botToken -- mirrors
// NewBotNotifier's own identical "single, statically-configured bot
// credential" precedent (notifier.go): a review session, like every
// other bot-ingress session, has no per-commenter OAuth token to reuse.
func NewVerdictNotifier(adapter *Adapter, botToken string) *VerdictNotifier {
	return &VerdictNotifier{adapter: adapter, botToken: botToken}
}

// Deliver implements ports.Notifier: decodes n.Payload as VerdictPayload,
// submits the formal review, then syncs labels (fetch current -> compute
// plan -> apply). n.Kind is not checked -- mirrors BotNotifier.Deliver's
// own identical "only ever asked to Deliver its own matching Kind in
// practice" precedent (the delivery worker's own kind->Notifier routing
// map is what guarantees that).
//
// Ordering (review first, then labels) is deliberate: the review is the
// substantive content a human reads; the labels are a secondary,
// purely-visual sync. If the label sync fails after the review already
// posted successfully, a retry re-attempts BOTH (this method has no
// partial-completion tracking of its own) -- CreateReview is not itself
// idempotent (a retried Deliver posts a SECOND formal review), a known,
// accepted limitation shared with every other Notifier in this codebase
// today (e.g. BotNotifier's own PostIssueComment is equally non-
// idempotent on retry) -- not a new gap this Step introduces. A label-sync
// failure never rolls back or otherwise undoes the review that already
// posted successfully.
func (n *VerdictNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	var payload VerdictPayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("githubapi: decode verdict payload: %w", err)
	}

	if err := n.adapter.CreateReview(ctx, payload.Owner, payload.Repo, payload.PRNumber, n.botToken,
		reviewpost.FormalReviewEvent(payload.Event), payload.Body); err != nil {
		return fmt.Errorf("githubapi: deliver verdict (create review): %w", err)
	}

	currentLabels, err := n.adapter.ListLabels(ctx, payload.Owner, payload.Repo, payload.PRNumber, n.botToken)
	if err != nil {
		return fmt.Errorf("githubapi: deliver verdict (list labels): %w", err)
	}
	plan := reviewpost.ComputeLabelSync(currentLabels, review.RiskLevel(payload.RiskLevel))

	if len(plan.Add) > 0 {
		if err := n.adapter.AddLabels(ctx, payload.Owner, payload.Repo, payload.PRNumber, n.botToken, plan.Add); err != nil {
			return fmt.Errorf("githubapi: deliver verdict (add labels): %w", err)
		}
	}
	for _, label := range plan.Remove {
		if err := n.adapter.RemoveLabel(ctx, payload.Owner, payload.Repo, payload.PRNumber, n.botToken, label); err != nil {
			return fmt.Errorf("githubapi: deliver verdict (remove label %q): %w", label, err)
		}
	}

	return nil
}
