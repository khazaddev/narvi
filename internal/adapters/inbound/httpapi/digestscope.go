// This file (digestscope.go) implements GET
// /api/repos/{owner}/{repo}/digest-scope (§12.2 item 5, §21.3): a
// read-only view of which Slack channels/Linear organizations would
// receive repoFullName's own next daily digest.
//
// §21.3's own design is explicit that the digest is "entirely
// deterministic" and its scope "per-repo/per-channel from day one, built
// entirely from EXISTING session-thread association tables" -- every
// channel that has recently threaded a review session for a repo
// receives that repo's own digest, full stop. There is no cadence knob
// (the pump runs on a fixed daily tick, internal/app/digest/pump.go) and
// no scope knob (scope is derived, never stored) for an admin to ever
// configure -- so this handler is deliberately READ-ONLY: it reuses the
// EXACT SAME derivation internal/app/digest's own real pump runs
// (postgres.DigestChannelStore.ListSlackChannels/ListLinearOrganizations,
// windowed by platform.Timeouts.DigestChannelDiscoveryLookback), rather
// than inventing a second, editable copy of a computed fact that would
// drift from what the pump actually does the moment either one changed
// independently.
//
// Gated by the existing authz.ActionViewAnalytics (§13.3 row 1: every
// role, including viewer, may read analytics) -- the SAME gate
// reviewanalytics.go's own GetReviewAnalytics uses, for the identical
// reason: this is a read over existing activity, not a management action.

package httpapi

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// GetRepoDigestScope backs GET /api/repos/{owner}/{repo}/digest-scope:
// 403 if the caller fails authz.ActionViewAnalytics; 404 if the URL's own
// {owner}/{repo} is not known to this deployment (resolveKnownRepo,
// mirroring every other repo-scoped route in this package); 200 with
// restdtos.RepoDigestScope otherwise.
func GetRepoDigestScope(channels *postgres.DigestChannelStore, prSessions *postgres.GitHubPRSessionStore, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionViewAnalytics, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		ctx := r.Context()
		logger := platform.Logger(ctx)
		lookback := timeouts.DigestChannelDiscoveryLookback
		since := pgtype.Timestamptz{Time: time.Now().Add(-lookback), Valid: true}

		slackChannels, err := channels.ListSlackChannels(ctx, repoFullName, since)
		if err != nil {
			logger.Error("httpapi: list slack digest channels failed", "error", err, "repo", repoFullName)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		linearOrgs, err := channels.ListLinearOrganizations(ctx, repoFullName, since)
		if err != nil {
			logger.Error("httpapi: list linear digest organizations failed", "error", err, "repo", repoFullName)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if slackChannels == nil {
			slackChannels = []string{}
		}
		if linearOrgs == nil {
			linearOrgs = []string{}
		}

		writeJSON(w, http.StatusOK, restdtos.RepoDigestScope{
			RepoFullName:          repoFullName,
			SlackChannelIds:       slackChannels,
			LinearOrganizationIds: linearOrgs,
			LookbackDays:          int(lookback.Hours() / 24),
		})
	}
}
