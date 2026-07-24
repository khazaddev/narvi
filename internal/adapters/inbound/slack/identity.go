// This file (identity.go) implements Step 39's ("identities + full RBAC",
// §13.2) own auto-linking wiring for Slack ingress -- shared by both
// handler.go (the Events API route) and interactive.go (the Interactivity
// route), replacing this package's PREVIOUS unconditional bot-attribution
// (an always-invalid creator/decidedBy/actor pgtype.UUID, every prior
// Step's own explicit precedent) with "try auto-link, else bot
// attribution + link-prompt side effect", per this Step's own brief.
//
// A plain function (not a method on either Deps or InteractiveDeps, which
// are two DISTINCT struct types in this package) since both call sites
// need the identical logic over just three collaborators
// (SlackClient/IdentityLink/Timeouts) -- passing those three directly
// avoids either duplicating this function once per Deps type or forcing
// one bloated shared struct neither file's own Deps otherwise needs.

package slack

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/platform"
)

// resolveSlackActor resolves slackUserID to a real Narvi user_id via
// internal/app/identitylink.Resolve, fetching that user's profile email
// (with retry, platform.Retry via identitylink.FetchEmailWithRetry) from
// Slack's own users.info API first -- ONLY when slackUserID is non-empty
// (every real app_mention/message/interactivity payload this package
// processes carries one; empty is a defensive no-op, never expected in
// practice).
//
// Returns (actorUserID, notice): actorUserID is Valid iff this identity is
// now known to belong to a real user (already linked, or auto-linked THIS
// call); notice is §13.2's own "notify in-channel" text (identitylink.
// Resolution.NotificationText), or "" when there's nothing to say -- the
// CALLER decides where to put it (handler.go appends it to the existing
// in-thread ack; interactive.go appends it to the plan-decision outcome
// text it already posts via chat.update).
func resolveSlackActor(ctx context.Context, logger *slog.Logger, slackClient *slackapi.Client, identityLinkDeps identitylink.Deps, timeouts platform.Timeouts, slackUserID string) (actorUserID pgtype.UUID, notice string) {
	if slackUserID == "" {
		return pgtype.UUID{}, ""
	}

	email, ok := identitylink.FetchEmailWithRetry(ctx, timeouts, func(attemptCtx context.Context) (string, bool, error) {
		e, o, err := slackClient.GetUserEmail(attemptCtx, slackUserID)
		if err != nil {
			if errors.Is(err, slackapi.ErrSlackUserNotFound) {
				// Slack's own definitive "no such user" -- never
				// retryable (see that sentinel's own doc comment).
				return "", false, platform.Permanent(err)
			}
			return "", false, err
		}
		return e, o, nil
	})

	res, err := identitylink.Resolve(ctx, identityLinkDeps, sqlcgen.IdentityProviderSlack, slackUserID, email, ok)
	if err != nil {
		logger.Warn("slack: identity auto-link resolve failed", "error", err, "slack_user_id", slackUserID)
		return pgtype.UUID{}, ""
	}
	return res.UserID, res.NotificationText()
}

// resolveSlackActorSingleAttempt is resolveSlackActor's own deliberately
// FASTER sibling, used ONLY by interactive.go's decideAndUpdateMessage/
// handleViewSubmission -- both share Slack's own hard ~3s interactivity-
// ack window with a real Postgres write (DecidePlan/CreateTurnCore) AND a
// second outbound Slack call (chat.update / views.open) inside the SAME
// budget (see platform.Timeouts.SlackInteractivityAckTimeout's own doc
// comment), so there is no room left for resolveSlackActor's own
// multi-attempt backoff loop (identitylink.FetchEmailWithRetry). This
// makes exactly ONE profile-email fetch attempt, bounded by fetchTimeout
// (platform.Timeouts.SlackInteractivityIdentityFetchTimeout in
// production wiring), and treats ANY failure (timeout, transient error,
// even Slack's own definitive "no such user") identically -- bot
// attribution for THIS click, deferring a full, properly-retried
// resolution to the next event from this same identity (an Events API
// message, a later click, a modal submission all naturally retry via
// resolveSlackActor's own full algorithm).
func resolveSlackActorSingleAttempt(ctx context.Context, logger *slog.Logger, slackClient *slackapi.Client, identityLinkDeps identitylink.Deps, fetchTimeout time.Duration, slackUserID string) (actorUserID pgtype.UUID, notice string) {
	if slackUserID == "" {
		return pgtype.UUID{}, ""
	}

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	email, ok, err := slackClient.GetUserEmail(fetchCtx, slackUserID)
	if err != nil {
		email, ok = "", false
	}

	res, err := identitylink.Resolve(ctx, identityLinkDeps, sqlcgen.IdentityProviderSlack, slackUserID, email, ok)
	if err != nil {
		logger.Warn("slack: identity auto-link resolve failed", "error", err, "slack_user_id", slackUserID)
		return pgtype.UUID{}, ""
	}
	return res.UserID, res.NotificationText()
}

// appendNotice appends notice to base on its own line, or returns base
// unchanged when notice is empty -- mirrors internal/adapters/inbound/
// linear's own identical helper (identity.go) exactly; kept as its own,
// separately-declared small function in each package (rather than a
// single exported one either would import from the other) since neither
// Slack nor Linear ingress otherwise depends on the other's own package at
// all, and this is a two-line function.
func appendNotice(base, notice string) string {
	if notice == "" {
		return base
	}
	return base + "\n\n" + notice
}
