// This file (mrkdwn.go) implements the INBOUND half of §8.10's
// "bidirectional mrkdwn contract" -- converting Slack's own mrkdwn markup
// (bold *text*, links <url|text>, user/channel mentions, HTML-entity
// escaping, ...) into the plain-text prompt httpapi.CreateSessionCore's
// own CreateSessionRequest.Prompt (and the reply-turn path's own Prompt)
// expect -- confirmed against contracts/rest/v1/dtos.schema.json's own
// CreateSessionRequest.prompt schema (a plain `["string","null"]`, no
// markdown-specific structure at all) before writing this converter,
// rather than guessing the target format. The OUTBOUND half (Narvi's own
// text -> Slack mrkdwn, for whatever §5.1's real Notifier eventually
// posts) is explicitly NOT this Step's job -- see doc.go's own scoping
// note.
//
// This is a best-effort normalizer, not a full mrkdwn parser: it targets
// exactly the constructs Slack's own real messages actually carry
// (confirmed against Slack's own formatting reference,
// docs.slack.dev/messaging/formatting-message-text, at this Step's own
// design time) that would otherwise read as noisy raw markup to an LLM
// prompt -- HTML-entity escaping, <...> link/mention wrapping, and the
// three emphasis markers. Fenced/inline code spans skip the
// markup-structural substitutions (bold/link/mention rewriting is never
// applied inside one, so a code sample's own literal *asterisks*/<angle
// brackets> are never rewritten) but still have HTML entities decoded,
// since Slack escapes &, <, > in the raw message text unconditionally,
// including inside code spans -- leaving a span's own "&lt;"/"&amp;"
// undecoded would bake literally-wrong text into the prompt.

package slack

import (
	"regexp"
	"strconv"
	"strings"
)

// codeSpanPattern matches a fenced ```code block``` (multiline) or an
// inline `code span` -- either form is extracted and restored verbatim
// around the rest of this file's own substitutions, so a code sample's
// own literal mrkdwn-shaped characters are never rewritten.
var codeSpanPattern = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")

// linkPattern matches Slack's own "<url>" and "<url|label>" link forms
// (docs.slack.dev/messaging/formatting-message-text: Slack always wraps a
// URL parsed from a message in angle brackets, with an optional
// "|label"). Deliberately excludes the special "<@...>"/"<#...>"/"<!...>"
// forms below via a negative lookahead-free ordering: this pattern only
// ever runs AFTER those three have already been substituted away (see
// normalizeMrkdwn), so nothing beginning with @/#/! reaches it.
var linkPattern = regexp.MustCompile(`<([^|>]+)\|([^>]*)>|<([^>]+)>`)

// userMentionPattern matches Slack's own "<@U0123ABC>" user-mention form.
var userMentionPattern = regexp.MustCompile(`<@([A-Z0-9]+)>`)

// channelMentionPattern matches Slack's own "<#C0123ABC>" and
// "<#C0123ABC|channel-name>" channel-reference forms.
var channelMentionPattern = regexp.MustCompile(`<#([A-Z0-9]+)(?:\|([^>]*))?>`)

// specialMentionPattern matches Slack's own "<!here>", "<!channel>",
// "<!everyone>" broadcast forms, and "<!subteam^S0123|@name>" user-group
// mentions.
var specialMentionPattern = regexp.MustCompile(`<!(here|channel|everyone)>|<!subteam\^[A-Z0-9]+\|([^>]*)>`)

// boldPattern/italicPattern/strikePattern match Slack's own single-
// delimiter emphasis markers, converted to their common-Markdown
// equivalents below (bold is the one genuinely different spelling;
// italic/strike already match common Markdown as-is, so those two exist
// mainly to document the mapping is intentional, not an oversight).
// Deliberately conservative: requires the delimiter pair on the SAME
// line with no embedded newline, so a bare "*" used as a list-bullet
// glyph on its own line is never mistaken for an opening bold marker.
var boldPattern = regexp.MustCompile(`\*([^*\n]+)\*`)

// htmlEntityReplacer undoes Slack's own required HTML-entity escaping
// (docs.slack.dev/messaging/formatting-message-text: Slack escapes &, <,
// > in message text sent to your app) -- order matters: &amp; must be
// unescaped LAST, otherwise "&amp;lt;" would incorrectly become "<"
// instead of the literal text "&lt;" the sender actually typed.
var htmlEntityReplacer = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// normalizeMrkdwn converts Slack's own mrkdwn-formatted message text into
// a clean, plain-text prompt string. Code spans (fenced or inline) are
// preserved verbatim; everything else has entities decoded, mention/link
// wrappers unwrapped into plain readable text, and emphasis markers
// converted to common Markdown.
func normalizeMrkdwn(text string) string {
	// Extract code spans first and replace each with a unique
	// placeholder so none of the markup-structural substitutions below
	// (bold/link/mention) ever reach inside one -- restored at the end.
	// Slack HTML-escapes &, <, > in the raw message text unconditionally,
	// including inside code spans, so entity decoding is the one
	// substitution that must still apply to a span's own contents here --
	// otherwise a code sample like "if (a < b)" would arrive as the
	// literal, undecoded "if (a &lt; b)" in the final prompt.
	var spans []string
	placeholder := codeSpanPattern.ReplaceAllStringFunc(text, func(span string) string {
		spans = append(spans, htmlEntityReplacer.Replace(span))
		return codeSpanPlaceholder(len(spans) - 1)
	})

	out := placeholder

	out = specialMentionPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := specialMentionPattern.FindStringSubmatch(m)
		if sub[1] != "" {
			// "<!here>"/"<!channel>"/"<!everyone>" -> "@here" etc.
			return "@" + sub[1]
		}
		// "<!subteam^S0123|@backend-team>" -- the label already carries
		// its own leading "@" (Slack's own real subteam-mention shape),
		// so it is used verbatim, not double-prefixed.
		return sub[2]
	})

	out = userMentionPattern.ReplaceAllString(out, "@$1")

	out = channelMentionPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := channelMentionPattern.FindStringSubmatch(m)
		if sub[2] != "" {
			return "#" + sub[2]
		}
		return "#" + sub[1]
	})

	out = linkPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := linkPattern.FindStringSubmatch(m)
		switch {
		case sub[1] != "":
			// "<url|label>" -> "label (url)"
			return sub[2] + " (" + sub[1] + ")"
		default:
			// "<url>" -> "url"
			return sub[3]
		}
	})

	out = boldPattern.ReplaceAllString(out, "**$1**")

	out = htmlEntityReplacer.Replace(out)

	// Restore code spans last, with entities already decoded above at
	// extraction time -- their own contents (which may contain literal
	// angle brackets, ampersands, asterisks, etc. once decoded) must
	// never pass through any of the markup-structural substitutions
	// above (bold/link/mention), only through entity decoding.
	for i, span := range spans {
		out = strings.Replace(out, codeSpanPlaceholder(i), span, 1)
	}

	return out
}

// codeSpanPlaceholder builds a unique, astronomically-unlikely-to-
// collide-with-real-message-text marker for extracted code span i.
func codeSpanPlaceholder(i int) string {
	return "\x00CODESPAN" + strconv.Itoa(i) + "\x00"
}
