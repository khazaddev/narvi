// Package digest implements §21.3's own deterministic daily digest --
// channel discovery (reusing existing session-thread association
// tables, never a second repo<->channel mechanism), rollup assembly from
// review_verdicts/auto_approval_outcomes (the SAME read model §21.1/
// §21.2 already build), claim-before-act send-state (digest_send_state,
// SELECT ... FOR UPDATE SKIP LOCKED), and outbox delivery reusing the
// existing Slack/Linear notifier machinery (internal/app/outboxworker) --
// never a second, parallel delivery path.
package digest

import (
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	appreviewverdict "github.com/narvidev/narvi/internal/app/reviewverdict"
	"github.com/narvidev/narvi/internal/platform"
)

// Deps bundles every dependency this package's own functions need --
// constructed once at process wiring time (cmd/control-plane/main.go).
type Deps struct {
	Channels  *postgres.DigestChannelStore
	SendState *postgres.DigestSendStateStore
	Outbox    *postgres.OutboxStore

	// ReviewVerdict is the SAME read-model bundle §21.1/§21.2 already
	// use (review_verdicts, repo_settings, auto_approval_outcomes) --
	// this package's own rollup is built from it directly, never a
	// second, independently-queried copy of the same facts.
	ReviewVerdict appreviewverdict.Deps

	Timeouts platform.Timeouts
}
