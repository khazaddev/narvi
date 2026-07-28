// This file (blockkit.go) implements Step 38's ("plan mode, cross-channel",
// §8.1/§13.3) own Slack-specific additions: real interactive Block Kit
// messages for a plan awaiting approval, chat.update to reflect a rendered
// decision on that same message (whichever channel actually decided), and
// views.open for the "Request changes" feedback modal. All three request/
// response shapes below were verified against Slack's own current, real
// Web API reference documentation during this Step's own investigation
// (docs.slack.dev/reference/methods/{chat.update,views.open},
// docs.slack.dev/reference/interaction-payloads/block_actions-payload) --
// not invented from a summary, matching this codebase's own established
// "verify against the real API" discipline (see client.go's own doc.go,
// Step 33's ack.go).
//
// # Button value encoding
//
// Each of the three buttons on the approval-request message (Approve &
// build / Request changes / Reject) carries the SAME EncodePlanActionValue
// output as its own "value" -- a small, delimited "planID|sessionID"
// string (deliberately not JSON: two UUIDs joined by a byte ('|') neither
// can ever contain, so no escaping is needed) -- rather than relying on
// Slack's own response_url alone, which Slack's docs describe as
// short-lived. internal/adapters/inbound/slack's own interactivity handler
// (interactive.go) is the ONLY other consumer of DecodePlanActionValue;
// living here, in the package that also constructs the value, keeps the
// encode/decode pair next to each other rather than splitting the format's
// definition across two packages.

package slackapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Block Kit interactive action_ids -- the fixed vocabulary this Step's own
// three approval-request buttons use, and the ONLY action_ids internal/
// adapters/inbound/slack's own block_actions dispatch (interactive.go)
// recognizes.
const (
	ActionApprovePlan        = "approve_plan"
	ActionRejectPlan         = "reject_plan"
	ActionRequestChangesPlan = "request_changes_plan"
)

// RequestChangesCallbackID/RequestChangesBlockID/RequestChangesActionID are
// the fixed identifiers the "Request changes" feedback modal (OpenView
// below) uses on its own single input block -- internal/adapters/inbound/
// slack's own view_submission handling (interactive.go) reads the
// submitted text back out by this SAME BlockID/ActionID pair.
const (
	RequestChangesCallbackID = "plan_request_changes"
	RequestChangesBlockID    = "feedback_block"
	RequestChangesActionID   = "feedback_input"
)

// planActionValueSeparator joins planID/sessionID into one button "value"
// string -- a byte neither UUID can ever contain, so this format needs no
// escaping and no JSON encode/decode overhead.
const planActionValueSeparator = "|"

// EncodePlanActionValue builds a Block Kit button's own "value" string
// identifying which plan/session it acts on -- the exact inverse of
// DecodePlanActionValue below.
func EncodePlanActionValue(planID, sessionID string) string {
	return planID + planActionValueSeparator + sessionID
}

// DecodePlanActionValue parses a button's own "value" string (as produced
// by EncodePlanActionValue) back into (planID, sessionID). ok is false for
// anything not shaped exactly "planID|sessionID" (defensive against a
// malformed/tampered payload -- Slack echoes back exactly what this
// package set, so this should be unreachable in practice, but a webhook
// body is untrusted input regardless of who claims to have sent it).
func DecodePlanActionValue(value string) (planID, sessionID string, ok bool) {
	parts := strings.SplitN(value, planActionValueSeparator, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// --- Block Kit block/element shapes -- deliberately the small subset this
// Step's own three messages need, not a general-purpose Block Kit library.

type textObject struct {
	Type string `json:"type"` // "plain_text" or "mrkdwn"
	Text string `json:"text"`
}

type sectionBlock struct {
	Type string      `json:"type"` // "section"
	Text *textObject `json:"text,omitempty"`
}

type dividerBlock struct {
	Type string `json:"type"` // "divider"
}

type contextBlock struct {
	Type     string       `json:"type"` // "context"
	Elements []textObject `json:"elements"`
}

type buttonElement struct {
	Type     string     `json:"type"` // "button"
	Text     textObject `json:"text"`
	ActionID string     `json:"action_id"`
	Value    string     `json:"value"`
	Style    string     `json:"style,omitempty"` // "primary"/"danger"/"" (default)
}

type actionsBlock struct {
	Type     string          `json:"type"` // "actions"
	Elements []buttonElement `json:"elements"`
}

// maxSectionTextRunes bounds how much of a plan's own rendered content this
// package ever embeds in one Block Kit section's own "text" -- Slack's own
// real limit for a section's plain_text/mrkdwn text object is 3000
// characters; this stays comfortably under that so the header/context/
// actions blocks around it are never at risk of pushing the WHOLE message
// over Slack's own separate, larger total-payload limit either.
const maxSectionTextRunes = 2800

// truncateForSection bounds text to maxSectionTextRunes runes, appending an
// honest "(truncated)" marker when it does -- never silently drops content
// without saying so.
func truncateForSection(text string) string {
	runes := []rune(text)
	if len(runes) <= maxSectionTextRunes {
		return text
	}
	return string(runes[:maxSectionTextRunes]) + "\n\n_(truncated)_"
}

// PlanApprovalPayload is the JSON shape this package expects to find in an
// outbox entry's own payload column for a ports.NotificationKindSlackPlanApproval
// row -- enqueued by internal/app/sessionactor at plan-mode turn-completion
// time (internal/app/sessionactor/outboxenqueue.go), carrying the plan's own
// identity, the originating thread's channel+thread_ts (from the session's
// own reverse-looked-up slack_thread_sessions row -- unchanged from how the
// existing generic Payload already sources these two fields), and Text (the
// plan's own rendered content -- steps/scope, best-effort extracted from the
// producing turn's own event stream; see outboxenqueue.go's own doc comment
// for the extraction this Step adds).
type PlanApprovalPayload struct {
	PlanID    string `json:"plan_id"`
	SessionID string `json:"session_id"`
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts"`
	Version   int    `json:"version"`
	Text      string `json:"text"`
}

// PlanDecidedPayload is the JSON shape this package expects to find in an
// outbox entry's own payload column for a ports.NotificationKindSlackPlanDecided
// row -- enqueued by httpapi.DecidePlanOnTx (decideplan.go) whenever a plan
// with a stored slack_channel_id/slack_message_ts transitions, regardless of
// which entry point (Slack itself, Linear, or web) actually decided it.
type PlanDecidedPayload struct {
	ChannelID string `json:"channel_id"`
	MessageTS string `json:"message_ts"`
	Text      string `json:"text"`
}

// postMessageWithBlocksRequest is chat.postMessage's own real request body
// shape when blocks are included -- Blocks is `[]any` (rather than a single
// concrete block type) since Block Kit blocks are a heterogeneous union;
// each element here is one of this file's own *Block/*Element struct
// literals, which json.Marshal renders correctly by its own concrete type.
type postMessageWithBlocksRequest struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Text     string `json:"text"`
	Blocks   []any  `json:"blocks,omitempty"`
}

// postMessageWithBlocksResponse is chat.postMessage's own real response
// envelope when the call succeeds: "ok", plus (per Slack's own documented
// response shape) the message's own real "channel" and "ts" -- this Step's
// own reason for calling chat.postMessage directly here rather than
// reusing Deliver's own plain-text Payload/postMessageRequest above: THIS
// caller needs channel+ts back, to persist onto the plans row (see this
// package's own PostPlanApprovalMessage doc comment).
type postMessageWithBlocksResponse struct {
	Ok      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel string `json:"channel"`
	Ts      string `json:"ts"`
}

// PostPlanApprovalMessage posts payload's plan-approval-request message
// with real interactive Block Kit buttons (Approve & build / Request
// changes / Reject, each carrying EncodePlanActionValue(payload.PlanID,
// payload.SessionID) as its own button value) into payload.ChannelID,
// threaded under payload.ThreadTS. Returns the message's own REAL channel+ts
// from Slack's own response (never derived/guessed) -- the caller (internal/
// app/outboxworker) persists these onto the plans row via
// PlanStore.SetSlackMessageRef so a later decision (from any entry point)
// can chat.update this exact message.
//
// Block composition (this package's own judgment, kept deliberately simple
// per this Step's own brief): a header section naming the version, a
// section carrying the plan's own rendered content (truncated via
// truncateForSection), a context line, a divider, then the actions row.
//
// Audit-fix batch (§8.10's own "missing outbound mrkdwn contract" finding):
// payload.Text is an LLM's own freeform Markdown, embedded here into a
// mrkdwn-typed Block Kit text object -- Markdown syntax like "**bold**" or
// "[text](url)" does not render in Slack's own mrkdwn dialect, so it is run
// through MarkdownToMrkdwn (mrkdwn_outbound.go) BEFORE truncation, so both
// the conversion and the length bound apply to what is actually rendered.
func (c *Client) PostPlanApprovalMessage(ctx context.Context, payload PlanApprovalPayload) (channel, ts string, err error) {
	value := EncodePlanActionValue(payload.PlanID, payload.SessionID)

	blocks := []any{
		sectionBlock{Type: "section", Text: &textObject{Type: "mrkdwn", Text: fmt.Sprintf("*Plan v%d ready for review*", payload.Version)}},
		sectionBlock{Type: "section", Text: &textObject{Type: "mrkdwn", Text: truncateForSection(MarkdownToMrkdwn(payload.Text))}},
		contextBlock{Type: "context", Elements: []textObject{
			{Type: "mrkdwn", Text: "Awaiting approval — first verdict wins, across Slack/Linear/web."},
		}},
		dividerBlock{Type: "divider"},
		actionsBlock{Type: "actions", Elements: []buttonElement{
			{Type: "button", Text: textObject{Type: "plain_text", Text: "Approve & build"}, ActionID: ActionApprovePlan, Value: value, Style: "primary"},
			{Type: "button", Text: textObject{Type: "plain_text", Text: "Request changes"}, ActionID: ActionRequestChangesPlan, Value: value},
			{Type: "button", Text: textObject{Type: "plain_text", Text: "Reject"}, ActionID: ActionRejectPlan, Value: value, Style: "danger"},
		}},
	}

	reqBody, err := json.Marshal(postMessageWithBlocksRequest{
		Channel:  payload.ChannelID,
		ThreadTS: payload.ThreadTS,
		Text:     fmt.Sprintf("Plan v%d is ready for review.", payload.Version), // notification-preview fallback text (Slack's own requirement whenever blocks are present)
		Blocks:   blocks,
	})
	if err != nil {
		return "", "", fmt.Errorf("slackapi: encode chat.postMessage (blocks) request: %w", err)
	}

	var parsed postMessageWithBlocksResponse
	if err := c.doPost(ctx, "/chat.postMessage", reqBody, &parsed); err != nil {
		return "", "", err
	}
	if !parsed.Ok {
		return "", "", &DeliveryError{SlackError: parsed.Error}
	}
	return parsed.Channel, parsed.Ts, nil
}

// chatUpdateRequest is chat.update's own real request body shape.
// Deliberately carries NO "blocks" field at all: Slack's own docs state
// that omitting blocks while supplying text REMOVES any existing blocks (so
// this Step never bothers constructing a "buttons removed" block set of its
// own -- the absence of Blocks here already achieves exactly that).
type chatUpdateRequest struct {
	Channel string `json:"channel"`
	Ts      string `json:"ts"`
	Text    string `json:"text"`
}

// UpdateMessage calls chat.update against an existing message (channel+ts,
// as returned by an earlier PostPlanApprovalMessage call and persisted via
// PlanStore.SetSlackMessageRef) to reflect a plan's final decided outcome --
// used both when Slack's own button click decided it (grey out/replace the
// buttons with the outcome) and when a DIFFERENT channel (Linear, web)
// decided it first (replace the still-pending buttons with an honest
// "already decided elsewhere" outcome line), AND for the plan-supersession
// notification (internal/app/sessionactor/planrecord.go's own audit-fix
// addition) -- all three share this one call site, so text is run through
// MarkdownToMrkdwn (mrkdwn_outbound.go, §8.10's own audit-fix finding)
// exactly once here rather than requiring each caller to remember to.
func (c *Client) UpdateMessage(ctx context.Context, channel, ts, text string) error {
	reqBody, err := json.Marshal(chatUpdateRequest{Channel: channel, Ts: ts, Text: MarkdownToMrkdwn(text)})
	if err != nil {
		return fmt.Errorf("slackapi: encode chat.update request: %w", err)
	}

	var parsed postMessageResponse
	if err := c.doPost(ctx, "/chat.update", reqBody, &parsed); err != nil {
		return err
	}
	if !parsed.Ok {
		return &DeliveryError{SlackError: parsed.Error}
	}
	return nil
}

// modalView is views.open's own real "view" payload shape (verified
// against Slack's own current views.open reference doc) -- deliberately
// only the fields this Step's own single-input feedback modal needs.
type modalView struct {
	Type            string     `json:"type"` // "modal"
	CallbackID      string     `json:"callback_id"`
	PrivateMetadata string     `json:"private_metadata"`
	Title           textObject `json:"title"`
	Submit          textObject `json:"submit"`
	Close           textObject `json:"close"`
	Blocks          []any      `json:"blocks"`
}

type plainTextInputElement struct {
	Type      string `json:"type"` // "plain_text_input"
	ActionID  string `json:"action_id"`
	Multiline bool   `json:"multiline"`
}

type inputBlock struct {
	Type    string                `json:"type"` // "input"
	BlockID string                `json:"block_id"`
	Label   textObject            `json:"label"`
	Element plainTextInputElement `json:"element"`
}

// viewsOpenRequest is views.open's own real top-level request body shape:
// trigger_id (Slack's own short-lived, ~3s-valid exchange token from the
// inbound block_actions payload that triggered this) plus the view payload
// itself.
type viewsOpenRequest struct {
	TriggerID string    `json:"trigger_id"`
	View      modalView `json:"view"`
}

// OpenView opens the "Request changes" feedback modal via a real
// views.open call, using triggerID from the inbound block_actions
// interaction that just fired (valid for only a few seconds -- this call
// must happen promptly, before responding to that interaction). planID/
// sessionID are encoded into the view's own private_metadata (max 255
// characters per Slack's own documented limit -- two UUIDs joined by
// EncodePlanActionValue's separator comfortably fits), so the LATER
// view_submission payload carries them back without a second DB round trip
// (this Step's own brief, point 2).
func (c *Client) OpenView(ctx context.Context, triggerID, planID, sessionID string) error {
	reqBody, err := json.Marshal(viewsOpenRequest{
		TriggerID: triggerID,
		View: modalView{
			Type:            "modal",
			CallbackID:      RequestChangesCallbackID,
			PrivateMetadata: EncodePlanActionValue(planID, sessionID),
			Title:           textObject{Type: "plain_text", Text: "Request changes"},
			Submit:          textObject{Type: "plain_text", Text: "Submit"},
			Close:           textObject{Type: "plain_text", Text: "Cancel"},
			Blocks: []any{
				inputBlock{
					Type:    "input",
					BlockID: RequestChangesBlockID,
					Label:   textObject{Type: "plain_text", Text: "What should change?"},
					Element: plainTextInputElement{
						Type:      "plain_text_input",
						ActionID:  RequestChangesActionID,
						Multiline: true,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("slackapi: encode views.open request: %w", err)
	}

	var parsed postMessageResponse
	if err := c.doPost(ctx, "/views.open", reqBody, &parsed); err != nil {
		return err
	}
	if !parsed.Ok {
		return &DeliveryError{SlackError: parsed.Error}
	}
	return nil
}

// postEphemeralRequest is chat.postEphemeral's own real request body shape
// (docs.slack.dev/reference/methods/chat.postEphemeral) -- channel + user
// (the ONE person who can ever see this message) + text, optionally
// threaded via thread_ts exactly like chatUpdateRequest above.
type postEphemeralRequest struct {
	Channel  string `json:"channel"`
	User     string `json:"user"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Text     string `json:"text"`
}

// PostEphemeral posts text into channel via chat.postEphemeral, visible
// ONLY to userID -- Step 39's own security-remediation addition
// ("identities + full RBAC", §13.2): a confirmed review finding proved
// that appending the magic-link identity-link notice to this package's
// own whole-channel-visible UpdateMessage/PostPlanApprovalMessage text let
// ANY other member of a shared channel who already had an authenticated
// Narvi web session open the link first and get the pending identity
// permanently linked to their OWN account instead of its rightful
// owner's. chat.postEphemeral is Slack's own documented mechanism for a
// message only the named user (never anyone else viewing the same
// channel/thread) can ever see -- used by internal/adapters/inbound/
// slack/interactive.go's own decideAndUpdateMessage to deliver that
// notice privately to the clicking user instead.
func (c *Client) PostEphemeral(ctx context.Context, channel, userID, threadTS, text string) error {
	reqBody, err := json.Marshal(postEphemeralRequest{Channel: channel, User: userID, ThreadTS: threadTS, Text: text})
	if err != nil {
		return fmt.Errorf("slackapi: encode chat.postEphemeral request: %w", err)
	}

	var parsed postMessageResponse
	if err := c.doPost(ctx, "/chat.postEphemeral", reqBody, &parsed); err != nil {
		return err
	}
	if !parsed.Ok {
		return &DeliveryError{SlackError: parsed.Error}
	}
	return nil
}

// doPost is the small shared HTTP mechanics every method in this file
// (PostPlanApprovalMessage/UpdateMessage/OpenView) uses: POST reqBody (a
// pre-marshaled JSON body) to c.apiBaseURL+path, authenticated with this
// Client's own bot token, decoding the bounded response body into out.
// Mirrors Deliver's own identical request/response mechanics (client.go) --
// factored out here since three new methods would otherwise repeat it
// three times.
func (c *Client) doPost(ctx context.Context, path string, reqBody []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("slackapi: build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slackapi: %s request failed: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return fmt.Errorf("slackapi: read %s response: %w", path, err)
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("slackapi: decode %s response: %w", path, err)
	}
	return nil
}
