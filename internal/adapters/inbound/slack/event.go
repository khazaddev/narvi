package slack

import "encoding/json"

// challengeEnvelope is the minimal shape this package decodes FIRST,
// before deciding how to handle the request any further -- just enough
// to recognize a "url_verification" handshake (doc.go's own step 4)
// without yet committing to the fuller eventEnvelope shape below (a
// url_verification request has no "event"/"event_id" fields at all).
type challengeEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
}

// eventEnvelope is Slack's real Events API outer envelope for a
// "type": "event_callback" request (confirmed against Slack's own
// current Events API documentation, docs.slack.dev/apis/events-api, at
// this Step's own design time) -- only the fields this adapter actually
// needs; Slack's own real payload also carries token/api_app_id/
// event_context/authorizations/is_ext_shared_channel, deliberately not
// modeled here since nothing in this package reads them.
type eventEnvelope struct {
	Type    string          `json:"type"`
	EventID string          `json:"event_id"`
	Event   json.RawMessage `json:"event"`
}

// slackEvent is the subset of Slack's own real "app_mention"/"message"
// event shapes (docs.slack.dev/reference/events/app_mention and the
// analogous message event reference, confirmed at this Step's own design
// time) this adapter needs. BotID is populated whenever the message was
// posted BY a bot (including this adapter's own in-thread ack) -- see
// doc.go's own step 6 for why that is filtered out unconditionally.
// Subtype is non-empty for anything that is not a plain new message
// (edits, deletes, channel-join notices, ...) and is likewise filtered.
type slackEvent struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	BotID    string `json:"bot_id"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

// threadKey returns the (channel_id, thread_ts) identity doc.go's own
// "Thread<->session mapping design" section describes: ev.ThreadTS when
// Slack supplied one (a genuine reply, or a mention inside a thread this
// adapter already knows about), otherwise ev.TS itself (this message
// becomes the root of a brand-new thread the moment this adapter's own
// ack replies to it).
func (ev slackEvent) threadKey() string {
	if ev.ThreadTS != "" {
		return ev.ThreadTS
	}
	return ev.TS
}

// messageClaimKey returns the (channel, ts) identity of the underlying
// Slack MESSAGE OBJECT this event describes -- L3 audit fix ("Slack's own
// dual-delivery for one logical mention isn't coalesced", handler.go's own
// slackMessageClaimProvider): Slack sends BOTH an app_mention event AND a
// message event (two distinct event_id values) for the SAME physical
// message, and both carry the IDENTICAL ts value, since they describe the
// same message object twice. Deliberately NOT threadKey()/ThreadTS above,
// which identifies the THREAD, not the individual message -- a genuine
// SECOND, different message posted later in the SAME thread carries a
// different ts and must NOT be coalesced with this one.
func (ev slackEvent) messageClaimKey() string {
	return ev.Channel + ":" + ev.TS
}

// isAppMention/isPlainMessage/isIgnorable implement doc.go's own step 6
// filtering: an event this adapter should never act on at all.
func (ev slackEvent) isAppMention() bool { return ev.Type == "app_mention" }

func (ev slackEvent) isPlainMessage() bool {
	return ev.Type == "message" && ev.Subtype == ""
}

func (ev slackEvent) isIgnorable() bool {
	if ev.BotID != "" {
		return true
	}
	if ev.Type == "message" && ev.Subtype != "" {
		return true
	}
	return !ev.isAppMention() && !ev.isPlainMessage()
}
