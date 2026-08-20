package automation

import (
	"errors"
	"fmt"
)

// TriggerType is WHAT causes an invocation to be created for an automation
// (§8.4: "GitHub/Linear/webhook/cron triggers with a condition builder") --
// matches the automation_trigger_type Postgres enum exactly (migrations/
// 000055_automations_triggers_and_extras.up.sql).
//
// A single trigger_type/trigger_config column pair, not an
// automation_triggers side table: mockups.html's own Automations view
// (v-autos, its own "Trigger" table column) shows exactly ONE trigger per
// automation row ("cron · 02:00 UTC", "github · pull_request.labeled"),
// never a list -- confirming a one-automation-to-one-trigger shape is the
// one this codebase's own design already assumes, not a guess.
type TriggerType string

const (
	// TriggerTypeManual is an automation with no automatic trigger of its
	// own -- fired only via a direct CreateInvocation call
	// (invocationenqueue.go), exactly as every automation created before
	// this Step already behaves (backfilled default, see this migration's
	// own doc comment). Also the correct value for an automation whose
	// only intended trigger is a future, still-unbuilt "Run now" manual UI
	// action (a deliberate scope note, mirrored from automation.go's
	// identical "no caller exists yet" precedent for TriggerResume).
	TriggerTypeManual TriggerType = "manual"
	// TriggerTypeCron is a schedule-driven trigger -- TriggerConfig.Cron
	// (CronTriggerConfig) names the schedule; app/automation's own trigger
	// pump evaluates it every tick via CronMatches.
	TriggerTypeCron TriggerType = "cron"
	// TriggerTypeGitHub is a GitHub webhook-event-driven trigger --
	// TriggerConfig.GitHub (GitHubTriggerConfig) names the event/action/
	// label filter, evaluated via MatchesGitHubTrigger. See this package's
	// own doc.go for why this Step models and validates this condition
	// fully but does not yet wire live dispatch into the existing GitHub
	// webhook ingress handler.
	TriggerTypeGitHub TriggerType = "github"
	// TriggerTypeLinear is a Linear webhook-event-driven trigger --
	// TriggerConfig.Linear (LinearTriggerConfig) names the event/action/
	// team filter, evaluated via MatchesLinearTrigger. Same doc.go note as
	// TriggerTypeGitHub applies.
	TriggerTypeLinear TriggerType = "linear"
	// TriggerTypeWebhook is a generic inbound-HTTP-call trigger: any
	// correctly bearer-token-authenticated POST to this automation's own
	// webhook endpoint fires it -- the "condition" IS authentication,
	// there is no further per-request filter to evaluate (unlike GitHub/
	// Linear's own event/action/label matchers). See internal/adapters/
	// inbound/httpapi's own automationwebhook.go.
	TriggerTypeWebhook TriggerType = "webhook"
)

// ErrUnknownTriggerType is ValidateTriggerType's own sentinel.
var ErrUnknownTriggerType = errors.New("automation: unknown trigger type")

// ValidateTriggerType reports whether t is one of the five recognized
// TriggerType values.
func ValidateTriggerType(t TriggerType) error {
	switch t {
	case TriggerTypeManual, TriggerTypeCron, TriggerTypeGitHub, TriggerTypeLinear, TriggerTypeWebhook:
		return nil
	default:
		return fmt.Errorf("automation: %w: %q", ErrUnknownTriggerType, string(t))
	}
}

// CronTriggerConfig is TriggerTypeCron's own trigger_config shape.
type CronTriggerConfig struct {
	// Schedule is a standard 5-field cron expression (cron.go's own
	// supported grammar) -- validated via ValidateCronExpr.
	Schedule string
}

// ErrEmptyCronSchedule is ValidateCronTriggerConfig's own sentinel for an
// empty Schedule -- distinct from cron.go's own ErrCronFieldCount/
// ErrCronFieldSyntax/ErrCronFieldRange (which all assume a non-empty
// candidate string was at least worth tokenizing).
var ErrEmptyCronSchedule = errors.New("automation: cron trigger config: schedule must not be empty")

// ValidateCronTriggerConfig validates cfg before it is accepted onto an
// automation with TriggerTypeCron.
func ValidateCronTriggerConfig(cfg CronTriggerConfig) error {
	if cfg.Schedule == "" {
		return ErrEmptyCronSchedule
	}
	if err := ValidateCronExpr(cfg.Schedule); err != nil {
		return fmt.Errorf("automation: cron trigger config: %w", err)
	}
	return nil
}

// GitHubTriggerConfig is TriggerTypeGitHub's own trigger_config shape: an
// automation fires when a GitHub webhook event arrives whose own
// EventType/Action/label set matches this filter. Modeled (and validated)
// in full here, as this Step's own §8.4 condition builder -- see this
// package's own doc.go for why live dispatch into the existing GitHub
// webhook ingress handler is deliberately deferred.
type GitHubTriggerConfig struct {
	// Event is GitHub's own webhook event-type name verbatim (e.g.
	// "pull_request", "issues", "issue_comment") -- required.
	Event string
	// Action is that event's own "action" payload field (e.g. "labeled",
	// "opened") -- "" (the zero value) means "any action for this Event
	// matches", never itself a required filter.
	Action string
	// Label, when non-empty, additionally requires GitHubEventInput.Labels
	// to contain this exact label name -- "" means "no label filter".
	Label string
}

// ErrEmptyGitHubEvent is ValidateGitHubTriggerConfig's own sentinel.
var ErrEmptyGitHubEvent = errors.New("automation: github trigger config: event must not be empty")

// ValidateGitHubTriggerConfig validates cfg before it is accepted onto an
// automation with TriggerTypeGitHub -- only Event is required; Action/Label
// are optional filters (see their own doc comments above).
func ValidateGitHubTriggerConfig(cfg GitHubTriggerConfig) error {
	if cfg.Event == "" {
		return ErrEmptyGitHubEvent
	}
	return nil
}

// GitHubEventInput is the minimal shape MatchesGitHubTrigger needs from a
// live GitHub webhook event -- a caller (a future dispatch wiring, per this
// package's own doc.go) derives this from the real webhook payload; this
// package never parses a raw GitHub payload itself (§11, adapter-
// independence).
type GitHubEventInput struct {
	EventType string
	Action    string
	Labels    []string
}

// MatchesGitHubTrigger reports whether in satisfies cfg's own filter: Event
// must match exactly; Action, if cfg.Action is non-empty, must also match
// exactly; Label, if cfg.Label is non-empty, must appear (exact string
// match) somewhere in in.Labels.
func MatchesGitHubTrigger(cfg GitHubTriggerConfig, in GitHubEventInput) bool {
	if cfg.Event != in.EventType {
		return false
	}
	if cfg.Action != "" && cfg.Action != in.Action {
		return false
	}
	if cfg.Label != "" {
		found := false
		for _, l := range in.Labels {
			if l == cfg.Label {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// LinearTriggerConfig is TriggerTypeLinear's own trigger_config shape --
// mirrors GitHubTriggerConfig's own shape, Linear's own vocabulary
// (EventType/Action/TeamKey rather than Event/Action/Label): an automation
// fires when a Linear webhook event arrives whose own EventType/Action/team
// matches this filter. Same "modeled and validated, live dispatch
// deferred" scope note as GitHubTriggerConfig applies -- see doc.go.
type LinearTriggerConfig struct {
	// EventType is Linear's own webhook event category verbatim (e.g.
	// "Issue", "Comment") -- required.
	EventType string
	// Action is that event's own action (e.g. "create", "update") -- ""
	// means "any action for this EventType matches".
	Action string
	// TeamKey, when non-empty, additionally requires LinearEventInput.
	// TeamKey to match exactly -- "" means "no team filter".
	TeamKey string
}

// ErrEmptyLinearEventType is ValidateLinearTriggerConfig's own sentinel.
var ErrEmptyLinearEventType = errors.New("automation: linear trigger config: event type must not be empty")

// ValidateLinearTriggerConfig validates cfg before it is accepted onto an
// automation with TriggerTypeLinear.
func ValidateLinearTriggerConfig(cfg LinearTriggerConfig) error {
	if cfg.EventType == "" {
		return ErrEmptyLinearEventType
	}
	return nil
}

// LinearEventInput is the minimal shape MatchesLinearTrigger needs from a
// live Linear webhook event -- see GitHubEventInput's own doc comment for
// the identical "caller derives this, this package never parses a raw
// payload" reasoning.
type LinearEventInput struct {
	EventType string
	Action    string
	TeamKey   string
}

// MatchesLinearTrigger reports whether in satisfies cfg's own filter --
// mirrors MatchesGitHubTrigger's own exact-match-per-populated-field logic.
func MatchesLinearTrigger(cfg LinearTriggerConfig, in LinearEventInput) bool {
	if cfg.EventType != in.EventType {
		return false
	}
	if cfg.Action != "" && cfg.Action != in.Action {
		return false
	}
	if cfg.TeamKey != "" && cfg.TeamKey != in.TeamKey {
		return false
	}
	return true
}
