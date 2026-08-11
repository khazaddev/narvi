package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
	digestdomain "github.com/khazaddev/narvi/internal/domain/digest"
	"github.com/khazaddev/narvi/internal/platform"
)

// maxClaimBatchSize bounds how many digest_send_state rows ONE PumpOnce
// call claims -- §21.1's own "bounded from day one" discipline, mirrors
// outboxworker.Builder's own identical per-tick claim batch bound
// (queries/outbox.sql's own ListDuePendingOutboxEntries).
const maxClaimBatchSize = 50

// Pump runs the digest's own background tick: discover channels, seed
// today's digest_send_state rows (idempotent), claim a batch, render and
// enqueue each claimed row's own outbox entry.
type Pump struct {
	deps Deps
}

// New builds a Pump.
func New(deps Deps) *Pump {
	return &Pump{deps: deps}
}

// Run ticks every deps.Timeouts.DigestPumpInterval until ctx is
// cancelled -- mirrors internal/app/automerge.Worker.Run's own identical
// ticker-loop shape.
func (p *Pump) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.deps.Timeouts.DigestPumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.PumpOnce(ctx, time.Now()); err != nil {
				platform.Logger(ctx).Error("digest: pump tick failed", "error", err)
			}
		}
	}
}

// PumpOnce runs one full tick -- see migrations/000071_digest_send_state.up.sql's
// own doc comment for the full two-phase (idempotent seed, then SKIP
// LOCKED claim) at-most-one-send-per-channel-per-day design this
// implements.
func (p *Pump) PumpOnce(ctx context.Context, now time.Time) error {
	logger := platform.Logger(ctx)
	sendDate := pgtype.Date{Time: now.UTC().Truncate(24 * time.Hour), Valid: true}

	channelRepos, err := discoverChannelRepos(ctx, p.deps, now)
	if err != nil {
		return fmt.Errorf("digest: discover channel repos: %w", err)
	}

	// Phase 1: idempotent seed -- every discovered channel gets a
	// 'pending' row for today, ON CONFLICT DO NOTHING (safe under
	// concurrent ticks/pods: whichever tick's INSERT lands first wins).
	for channel := range channelRepos {
		if _, _, err := p.deps.SendState.Seed(ctx, sendDate, string(channel.Provider), channel.ID); err != nil {
			logger.Error("digest: seed send state failed", "error", err, "provider", channel.Provider, "channel_id", channel.ID)
		}
	}

	// Phase 2: claim -- SELECT ... FOR UPDATE SKIP LOCKED + UPDATE to
	// 'sending', a single round trip (ClaimPendingDigestSendState's own
	// generated doc comment). This IS the at-most-one-send guarantee:
	// two concurrent ticks racing the SAME row can never both see it as
	// claimable.
	claimed, err := p.deps.SendState.ClaimPending(ctx, sendDate, maxClaimBatchSize)
	if err != nil {
		return fmt.Errorf("digest: claim pending send state: %w", err)
	}

	for _, row := range claimed {
		p.sendOne(ctx, row, channelRepos, now)
	}
	return nil
}

// sendOne renders and enqueues one already-claimed row's own digest --
// every failure marks the row 'failed' (terminal for today, never
// re-claimed the same day, MarkDigestSendStateFailed's own generated doc
// comment) rather than leaving it stuck 'sending' forever.
func (p *Pump) sendOne(ctx context.Context, row sqlcgen.DigestSendState, channelRepos map[Channel][]string, now time.Time) {
	logger := platform.Logger(ctx)
	channel := Channel{Provider: digestdomain.Provider(row.ChannelProvider), ID: row.ChannelID}

	// repos comes from THIS tick's own discovery pass, matching the row
	// this SAME tick just seeded/claimed -- a channel claimed from an
	// EARLIER tick's own seed (this tick found it already 'pending' but
	// did not re-seed, ON CONFLICT DO NOTHING) falls back to a fresh,
	// same-tick discovery lookup so its own repo set is never empty
	// just because this tick's map only holds channels IT discovered.
	repos := channelRepos[channel]
	if len(repos) == 0 {
		repos = rediscoverReposForChannel(ctx, p.deps, channel, now)
	}

	rollup := buildRollup(ctx, p.deps, row.SendDate.Time, repos, now)
	text := digestdomain.Render(rollup, channel.Provider)

	kind, payload, err := buildNotificationPayload(channel, text)
	if err != nil {
		logger.Error("digest: build notification payload failed", "error", err, "provider", channel.Provider, "channel_id", channel.ID)
		p.markFailed(ctx, row.ID, err.Error())
		return
	}

	if _, err := p.deps.Outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{Kind: string(kind), Payload: payload}); err != nil {
		logger.Error("digest: enqueue outbox entry failed", "error", err, "provider", channel.Provider, "channel_id", channel.ID)
		p.markFailed(ctx, row.ID, err.Error())
		return
	}

	if err := p.deps.SendState.MarkSent(ctx, row.ID); err != nil {
		// The outbox row already exists and WILL be delivered -- a
		// failure marking send_state 'sent' here must never be treated
		// as though the digest itself failed; it only risks THIS row
		// remaining claimable-looking ('sending') until a future tick's
		// own claim query simply skips it anyway (it is no longer
		// 'pending'). Logged, not retried -- mirrors httpapi.
		// MergePullRequest's own "the merge already happened, a logging
		// failure must never claim otherwise" posture for the analogous
		// post-success bookkeeping write.
		logger.Error("digest: mark send state sent failed", "error", err, "provider", channel.Provider, "channel_id", channel.ID)
	}
}

func (p *Pump) markFailed(ctx context.Context, id pgtype.UUID, reason string) {
	if err := p.deps.SendState.MarkFailed(ctx, id, reason); err != nil {
		platform.Logger(ctx).Error("digest: mark send state failed failed", "error", err)
	}
}

// rediscoverReposForChannel re-derives ONE channel's own repo set on
// demand -- the fallback path for a row claimed this tick that this
// SAME tick's own discoverChannelRepos call did not happen to enumerate
// (e.g. seeded by an earlier tick, still pending because an earlier
// claim attempt failed before reaching MarkSent/MarkFailed). Best-effort:
// an error here still renders a digest, just possibly with zero repo
// sections (channelRepos' own doc comment on "a channel with no repos
// gets no sections" is a legitimate, honest degrade, never a reason to
// drop the send entirely).
func rediscoverReposForChannel(ctx context.Context, deps Deps, channel Channel, now time.Time) []string {
	since := pgtype.Timestamptz{Time: now.Add(-deps.Timeouts.DigestChannelDiscoveryLookback), Valid: true}
	repos, err := deps.Channels.ListDistinctRepos(ctx, since, maxReposPerDiscoveryTick)
	if err != nil {
		platform.Logger(ctx).Error("digest: re-discover repos for channel failed", "error", err, "provider", channel.Provider, "channel_id", channel.ID)
		return nil
	}

	var matched []string
	for _, repo := range repos {
		var channelIDs []string
		var listErr error
		switch channel.Provider {
		case digestdomain.ProviderSlack:
			channelIDs, listErr = deps.Channels.ListSlackChannels(ctx, repo, since)
		case digestdomain.ProviderLinear:
			channelIDs, listErr = deps.Channels.ListLinearOrganizations(ctx, repo, since)
		}
		if listErr != nil {
			continue
		}
		for _, id := range channelIDs {
			if id == channel.ID {
				matched = append(matched, repo)
				break
			}
		}
	}
	return matched
}

// buildNotificationPayload marshals text for channel's own provider into
// the exact outbox Kind/payload shape that provider's notifier expects.
func buildNotificationPayload(channel Channel, text string) (ports.NotificationKind, []byte, error) {
	switch channel.Provider {
	case digestdomain.ProviderSlack:
		payload, err := json.Marshal(slackapi.DigestPayload{ChannelID: channel.ID, Text: text})
		if err != nil {
			return "", nil, fmt.Errorf("digest: marshal slack digest payload: %w", err)
		}
		return ports.NotificationKindSlackDigest, payload, nil
	case digestdomain.ProviderLinear:
		payload, err := json.Marshal(struct {
			OrganizationID string `json:"organization_id"`
			Text           string `json:"text"`
		}{OrganizationID: channel.ID, Text: text})
		if err != nil {
			return "", nil, fmt.Errorf("digest: marshal linear digest payload: %w", err)
		}
		return ports.NotificationKindLinearDigest, payload, nil
	default:
		return "", nil, fmt.Errorf("digest: unrecognized channel provider %q", channel.Provider)
	}
}
