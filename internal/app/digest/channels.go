package digest

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	digestdomain "github.com/khazaddev/narvi/internal/domain/digest"
	"github.com/khazaddev/narvi/internal/platform"
)

// maxReposPerDiscoveryTick bounds Pump's own per-tick repo enumeration --
// §21.1's own "bounded from day one" discipline, mirrors
// internal/app/automerge's own identical per-tick repo cap (there, via
// repo_settings; here, via github_pr_sessions, since a digest is not
// gated by any per-repo toggle -- every repo with recent activity gets
// one).
const maxReposPerDiscoveryTick = 500

// Channel is one (provider, external id) destination -- a Slack channel
// ID or a Linear organization ID, the SAME closed, two-value vocabulary
// internal/domain/digest.Provider already establishes.
type Channel struct {
	Provider digestdomain.Provider
	ID       string
}

// discoverChannelRepos maps every channel with recent review activity to
// the set of repos that activity belongs to -- §21.3's own "per-repo/
// per-channel" scoping, built ENTIRELY from existing session-thread
// association tables (slack_thread_sessions/linear_agent_sessions,
// joined through github_pr_sessions) -- see queries/digestchannels.sql's
// own doc comment for why this reuses rather than invents a repo<->channel
// mechanism. A repo with recent activity but no channel at all (no
// review session for it was ever threaded through Slack/Linear)
// contributes no entry -- there is nowhere to send its own digest.
func discoverChannelRepos(ctx context.Context, deps Deps, now time.Time) (map[Channel][]string, error) {
	since := pgtype.Timestamptz{Time: now.Add(-deps.Timeouts.DigestChannelDiscoveryLookback), Valid: true}

	repos, err := deps.Channels.ListDistinctRepos(ctx, since, maxReposPerDiscoveryTick)
	if err != nil {
		return nil, err
	}

	result := make(map[Channel][]string)
	logger := platform.Logger(ctx)
	for _, repo := range repos {
		slackChannels, err := deps.Channels.ListSlackChannels(ctx, repo, since)
		if err != nil {
			// Best-effort per-repo, per-provider -- one repo's own
			// discovery failure never blocks every other repo's tick
			// (mirrors internal/app/decisioninbox's own buildAttentionItems
			// "each sub-scan independently best-effort" precedent).
			logger.Error("digest: list slack channels for repo failed", "error", err, "repo_full_name", repo)
		}
		for _, channelID := range slackChannels {
			key := Channel{Provider: digestdomain.ProviderSlack, ID: channelID}
			result[key] = append(result[key], repo)
		}

		linearOrgs, err := deps.Channels.ListLinearOrganizations(ctx, repo, since)
		if err != nil {
			logger.Error("digest: list linear organizations for repo failed", "error", err, "repo_full_name", repo)
		}
		for _, orgID := range linearOrgs {
			key := Channel{Provider: digestdomain.ProviderLinear, ID: orgID}
			result[key] = append(result[key], repo)
		}
	}
	return result, nil
}
