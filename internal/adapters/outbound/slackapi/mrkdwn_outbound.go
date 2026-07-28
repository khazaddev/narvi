// This file (mrkdwn_outbound.go) implements the OUTBOUND half of §8.10's
// "bidirectional mrkdwn contract" -- converting common Markdown (as
// produced by an LLM's own freeform plan/decision text -- see
// internal/app/sessionactor/planapprovalcontent.go's own doc comment for
// where PlanApprovalPayload.Text/PlanDecidedPayload.Text ultimately come
// from) into Slack's own mrkdwn dialect before it is ever embedded into a
// Block Kit {Type: "mrkdwn", ...} text object or a chat.update "text"
// parameter.
//
// The INBOUND half (internal/adapters/inbound/slack/mrkdwn.go's own
// normalizeMrkdwn) goes the OPPOSITE direction -- Slack mrkdwn syntax
// (<url|text>, <@U123>, single-asterisk *bold*) into plain text for an LLM
// prompt -- and is NOT reusable in reverse: it unwraps Slack's own already-
// mrkdwn syntax rather than producing it, and converts single-asterisk
// *bold* into DOUBLE-asterisk **bold** (i.e. towards common Markdown, the
// exact opposite of what this file needs). This file is a genuinely new,
// independent converter for the other direction.
//
// This is a best-effort, deliberately small converter, not a full CommonMark
// parser: it targets exactly the handful of constructs a plan-mode turn's
// own LLM-generated content actually carries in practice (bold emphasis,
// links, headings, and list items) -- confirmed against Slack's own
// mrkdwn formatting reference (docs.slack.dev/messaging/formatting-message-
// text) before writing this, per this codebase's own established "verify
// against the real API/format before writing a converter" discipline (see
// mrkdwn.go's own identical precedent for the inbound direction).
//
// # Entity escaping (audit-fix batch: literal Slack-special characters)
//
// Slack's own mrkdwn dialect reserves bare "&", "<", ">" for its own syntax
// (entity references, <url|text> links, <!channel>/<@U123> special
// mentions) -- exactly like HTML. The input to this converter is an LLM's
// own freeform, ordinary Markdown prose; it was never written with Slack's
// dialect in mind, so any bare "&"/"<"/">" it happens to contain (ordinary
// technical prose like "latency < 200ms", or -- worse -- a literal,
// unintentionally-quoted "<!channel>" substring) is ALWAYS incidental plain
// text, never an intentional Slack construct the LLM meant to produce.
// escapeMrkdwnEntities therefore runs FIRST, unconditionally, on the whole
// raw input, turning every such character into its "&amp;"/"&lt;"/"&gt;"
// entity form -- before any of this file's own bold/link/heading syntax
// conversion runs. Every later conversion step below then both (a) reads
// already-escaped text for whatever it captures out of the source (so a
// literal ">" trapped inside a converted link's own label/URL is already
// "&gt;", never breaking the <url|text> tag Slack terminates at the first
// raw ">" it sees) and (b) only ever WRITES genuinely raw, unescaped
// "<"/">"/"|" for the new Slack-syntax sequences it intentionally
// constructs (e.g. mdLinkPattern's own "<$2|$1>" replacement) -- since
// escapeMrkdwnEntities runs exactly once, before any of that construction,
// this file's own generated syntax is never itself re-escaped into
// something broken.
//
// Coverage and its own deliberate limits, decided rather than guessed:
//   - Bare "&", "<", ">" anywhere in the source -> "&amp;", "&lt;", "&gt;"
//     (see above) -- applies everywhere, including inside a converted
//     link's own label/URL text.
//   - **bold** / __bold__ (common Markdown's two bold spellings) -> Slack
//     mrkdwn's single-asterisk *bold*.
//   - [text](url) -> Slack mrkdwn's <url|text> link form. A literal "|"
//     inside the captured label is deliberately left unescaped -- Slack's
//     own <url|text> parser splits on only the FIRST "|" it finds inside
//     the tag, so a "|" appearing later, inside the label itself, is
//     already correctly treated as part of the label text, not a second
//     separator; there is no HTML-style entity for "|" in Slack's mrkdwn
//     dialect to escape it into regardless.
//   - # / ## / ... ATX headings -> Slack mrkdwn has NO heading syntax at
//     all; the best honest degradation is rendering the heading text as a
//     bold line (*Heading*), which at least visually sets it apart from
//     body text -- not a perfect substitute, but never silently dropped or
//     left as raw, meaningless "#" characters in the rendered message
//     either.
//   - "- item" / "* item" list markers are left UNCHANGED, a deliberate
//     decision (not an oversight): Slack's own mrkdwn renders a leading
//     "- "/"* " at the start of a line reasonably as plain text with a
//     literal dash/asterisk glyph -- readable as a list to a human reader
//     even without Slack's own real bullet rendering, which mrkdwn has no
//     syntax for either. Converting it into anything else (e.g. a bullet
//     unicode character) would be inventing a transformation Slack's own
//     format neither offers nor requires.
//   - Anything else (tables, nested/ordered lists, code fences, blockquotes,
//     ...) passes through unchanged -- not a construct this converter
//     claims to handle, and not observed in practice in this codebase's own
//     plan-mode content.

package slackapi

import (
	"regexp"
	"strings"
)

// mdLinkPattern matches common Markdown's "[text](url)" link syntax. Run
// BEFORE mdBoldPattern below so a link's own "text" half may itself still
// contain "**bold**" and have that converted afterward (Slack's own
// <url|text> form permits mrkdwn markup inside the "text" half).
var mdLinkPattern = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)

// mdBoldPattern matches common Markdown's two bold-emphasis spellings --
// "**bold**" and "__bold__" -- capturing whichever one matched in group 1
// or group 2 respectively.
var mdBoldPattern = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)

// mdHeadingPattern matches an ATX heading line ("# ", "## ", ... "###### "),
// anchored to the start/end of each line (regexp's (?m) flag) since a
// heading is a whole-line construct, never an inline span.
var mdHeadingPattern = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.*)$`)

// escapeMrkdwnEntities escapes the three characters Slack's own mrkdwn
// dialect reserves for its own syntax -- "&", "<", ">" -- into their
// "&amp;"/"&lt;"/"&gt;" entity forms, exactly as Slack's own mrkdwn
// formatting reference requires for any literal occurrence of these
// characters (see this file's own top doc comment's "Entity escaping"
// section for why this must run FIRST, before any of this file's own
// syntax conversion). "&" is escaped BEFORE "<"/">" so the ampersand
// INSIDE the "&lt;"/"&gt;" entities this function itself just produced is
// never escaped a second time.
func escapeMrkdwnEntities(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// MarkdownToMrkdwn converts a best-effort subset of common Markdown (bold,
// links, ATX headings) into Slack's own mrkdwn dialect -- see this file's
// own top doc comment for exactly what is and is not handled, and why.
// Wired into PostPlanApprovalMessage's own payload.Text embedding and
// UpdateMessage's own text parameter (blockkit.go) so every caller of
// either -- every existing real approve/reject decision, and the new
// plan-supersession notification (internal/app/sessionactor/planrecord.go)
// alike -- gets the conversion automatically, with no caller needing to
// remember to apply it separately.
func MarkdownToMrkdwn(text string) string {
	// Escape any PRE-EXISTING literal "&"/"<"/">" in the raw source FIRST,
	// before any Slack syntax of our own is generated below -- see this
	// file's own top doc comment. Every later step below reads from (and
	// only ever adds new raw "<"/">"/"|" on top of) this already-escaped
	// text, so this file's own generated <url|text> syntax is never itself
	// re-escaped.
	out := escapeMrkdwnEntities(text)

	out = mdLinkPattern.ReplaceAllString(out, "<$2|$1>")

	out = mdBoldPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := mdBoldPattern.FindStringSubmatch(m)
		if sub[1] != "" {
			return "*" + sub[1] + "*"
		}
		return "*" + sub[2] + "*"
	})

	out = mdHeadingPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := mdHeadingPattern.FindStringSubmatch(m)
		content := sub[1]
		if len(content) > 1 && strings.HasPrefix(content, "*") && strings.HasSuffix(content, "*") {
			// The heading's own text was already fully mrkdwn-bold (e.g. a
			// heading whose Markdown source was itself "# **Title**",
			// already turned into "*Title*" by the bold pass above) --
			// don't wrap it in a SECOND layer of asterisks, which Slack
			// would render as literal asterisk characters around bold text
			// rather than plain bold.
			return content
		}
		return "*" + content + "*"
	})

	return out
}
