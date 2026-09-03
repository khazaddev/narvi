package slackapi_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/slackapi"
)

// TestMarkdownToMrkdwn_Conversions proves this package's own FIRST EVER
// outbound-direction mrkdwn conversion (§8.10's own audit-fix finding): the
// common-Markdown constructs a plan-mode turn's own LLM-generated content
// actually carries (bold, links, ATX headings, list markers) each convert
// to their real Slack mrkdwn equivalent -- or, for headings (which mrkdwn
// has no syntax for at all) and lists (left deliberately unchanged), the
// specific, deliberate degradation this file's own doc comment documents.
func TestMarkdownToMrkdwn_Conversions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "double-asterisk bold",
			in:   "This is **bold** text.",
			want: "This is *bold* text.",
		},
		{
			name: "double-underscore bold",
			in:   "This is __bold__ text.",
			want: "This is *bold* text.",
		},
		{
			name: "multiple bold spans",
			in:   "**one** and **two**",
			want: "*one* and *two*",
		},
		{
			name: "markdown link",
			in:   "See [the docs](https://example.com/docs) for more.",
			want: "See <https://example.com/docs|the docs> for more.",
		},
		{
			name: "multiple links",
			in:   "[a](https://a.example) and [b](https://b.example)",
			want: "<https://a.example|a> and <https://b.example|b>",
		},
		{
			name: "h1 heading degrades to bold line",
			in:   "# Plan overview",
			want: "*Plan overview*",
		},
		{
			name: "h3 heading degrades to bold line",
			in:   "### §5.4: migrate the schema",
			want: "*§5.4: migrate the schema*",
		},
		{
			name: "heading among body text",
			in:   "# Summary\n\nThe plan does X.",
			want: "*Summary*\n\nThe plan does X.",
		},
		{
			name: "dash list markers pass through unchanged",
			in:   "- first step\n- second step",
			want: "- first step\n- second step",
		},
		{
			name: "asterisk list markers pass through unchanged (not mistaken for bold)",
			in:   "* first step\n* second step",
			want: "* first step\n* second step",
		},
		{
			name: "combined heading, bold, and link",
			in:   "# Plan v2\n\n- Update **auth.go** per [the ticket](https://example.com/ISSUE-1)\n- Ship it",
			want: "*Plan v2*\n\n- Update *auth.go* per <https://example.com/ISSUE-1|the ticket>\n- Ship it",
		},
		{
			name: "plain text with no markdown is unchanged",
			in:   "Nothing special here, just plain text.",
			want: "Nothing special here, just plain text.",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},

		// --- Audit-fix batch: entity escaping of pre-existing literal
		// Slack-special characters (HIGH finding) ---

		{
			name: "ordinary technical prose with bare < and > is escaped, not mangled",
			in:   "latency < 200ms and throughput > 500rps",
			want: "latency &lt; 200ms and throughput &gt; 500rps",
		},
		{
			name: "bare ampersand is escaped",
			in:   "A & B",
			want: "A &amp; B",
		},
		{
			name: "a literal <!channel> substring never becomes a live @channel broadcast",
			in:   "As discussed in <!channel> earlier, ship it.",
			want: "As discussed in &lt;!channel&gt; earlier, ship it.",
		},
		{
			name: "a literal <@U123> substring never becomes a live user mention",
			in:   "cc <@U12345> for review",
			want: "cc &lt;@U12345&gt; for review",
		},
		{
			name: "escaping runs before heading conversion without interfering",
			in:   "# Latency < 200ms & throughput > 500rps",
			want: "*Latency &lt; 200ms &amp; throughput &gt; 500rps*",
		},
		{
			name: "escaping runs before bold conversion without interfering",
			in:   "**A < B & C > D**",
			want: "*A &lt; B &amp; C &gt; D*",
		},

		// --- Audit-fix batch: link label/URL escaping (MEDIUM finding) ---

		{
			name: "link whose label contains > still produces a validly-terminated tag",
			in:   "[latency > 200ms](https://dashboard.example.com/metric)",
			want: "<https://dashboard.example.com/metric|latency &gt; 200ms>",
		},
		{
			name: "link whose label contains < and & is also escaped",
			in:   "[A < B & C](https://example.com/x)",
			want: "<https://example.com/x|A &lt; B &amp; C>",
		},
		{
			name: "link whose URL contains a literal ampersand query separator is escaped, and this file's own generated <url|text> syntax is never re-escaped",
			in:   "[docs](https://example.com/a?x=1&y=2)",
			want: "<https://example.com/a?x=1&amp;y=2|docs>",
		},
		{
			name: "link whose label contains a literal pipe still renders correctly (Slack splits on only the first |)",
			in:   "[A|B](https://example.com/x)",
			want: "<https://example.com/x|A|B>",
		},

		// --- Audit-fix batch: link-target allowlist (HIGH finding) ---
		// A Markdown link's own TARGET, not just its label, must be
		// checked before being wrapped in Slack's live <target|label>
		// syntax -- otherwise a target that happens to look like one of
		// Slack's own special forms (a bare "!channel" broadcast, a
		// "#anchor" channel-reference shape, or an "@U123" user mention)
		// becomes a real, live Slack tag purely because the converter
		// wrapped it in "<...>" itself, with no raw "<"/">" literal in
		// the source for escapeMrkdwnEntities to have caught.

		{
			name: "confirmed reproduction: a link target of bare !channel must never become a live @channel broadcast",
			in:   "Please review and [ping the team](!channel) once ready.",
			want: "Please review and ping the team (!channel) once ready.",
		},
		{
			name: "an internal anchor link target must never become a live #channel-reference tag",
			in:   "# [Overview](#overview)",
			want: "*Overview (#overview)*",
		},
		{
			name: "a link target of bare @U123 must never become a live user mention",
			in:   "[cc the owner](@U0123ABCD)",
			want: "cc the owner (@U0123ABCD)",
		},
		{
			name: "a genuine https:// link target still converts normally (the fix must not over-correct)",
			in:   "[text](https://example.com/path)",
			want: "<https://example.com/path|text>",
		},
		{
			name: "a genuine http:// link target still converts normally",
			in:   "[docs](http://example.com/docs)",
			want: "<http://example.com/docs|docs>",
		},
		{
			name: "a mailto: link target still converts normally",
			in:   "[email us](mailto:team@example.com)",
			want: "<mailto:team@example.com|email us>",
		},
		{
			name: "a relative path link target is rendered as safe plain text, not linkified",
			in:   "[see here](../docs/readme.md)",
			want: "see here (../docs/readme.md)",
		},

		// --- Audit-fix batch: link-target pipe smuggling (MEDIUM finding) ---
		// A link target containing a raw "|" must never be linkified, even
		// when it otherwise starts with an allowed scheme -- Slack's own
		// "<target|label>" grammar splits on only the FIRST "|" inside the
		// tag, so a target's own embedded "|" would smuggle the rest of the
		// target (and anything after it) into the rendered, visible label.

		{
			name: "confirmed reproduction: a link target containing a raw pipe must fall back to safe plain text, never producing a tag with more than one | inside it",
			in:   "[Click here](https://good.example.com/path|!channel)",
			want: "Click here (https://good.example.com/path|!channel)",
		},
		{
			name: "an otherwise-valid https target containing a raw pipe in a query string also falls back to safe plain text",
			in:   "[docs](https://example.com/x?a=1|b=2)",
			want: "docs (https://example.com/x?a=1|b=2)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := slackapi.MarkdownToMrkdwn(tc.in)
			if got != tc.want {
				t.Errorf("MarkdownToMrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
