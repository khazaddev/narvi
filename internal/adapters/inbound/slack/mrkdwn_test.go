package slack

import "testing"

// TestNormalizeMrkdwn is table-driven over the constructs Slack's own
// real messages actually carry (see mrkdwn.go's own doc comment): HTML
// entity escaping, the three <...>-wrapped forms (link/user-mention/
// channel-mention/special-mention), single-asterisk bold, and code-span
// preservation.
func TestNormalizeMrkdwn(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text unchanged",
			in:   "please fix the failing check",
			want: "please fix the failing check",
		},
		{
			name: "html entities decoded",
			in:   "a &amp; b &lt;tag&gt;",
			want: "a & b <tag>",
		},
		{
			name: "user mention unwrapped",
			in:   "<@U0LAN0Z89> can you look at this?",
			want: "@U0LAN0Z89 can you look at this?",
		},
		{
			name: "channel mention with label",
			in:   "see <#C123ABC|general> for context",
			want: "see #general for context",
		},
		{
			name: "channel mention without label",
			in:   "see <#C123ABC> for context",
			want: "see #C123ABC for context",
		},
		{
			name: "special mention here",
			in:   "<!here> please review",
			want: "@here please review",
		},
		{
			name: "special mention everyone",
			in:   "<!everyone> heads up",
			want: "@everyone heads up",
		},
		{
			name: "subteam mention",
			in:   "<!subteam^S0123|@backend-team> please triage",
			want: "@backend-team please triage",
		},
		{
			name: "link with label",
			in:   "see <https://example.com/doc|the doc> for details",
			want: "see the doc (https://example.com/doc) for details",
		},
		{
			name: "bare link",
			in:   "see <https://example.com/doc> for details",
			want: "see https://example.com/doc for details",
		},
		{
			name: "bold converted to double asterisk",
			in:   "this is *urgent* please",
			want: "this is **urgent** please",
		},
		{
			name: "inline code span preserved verbatim",
			in:   "run `git status && echo *not-bold*` now",
			want: "run `git status && echo *not-bold*` now",
		},
		{
			name: "fenced code block preserved verbatim",
			in:   "```\nfunc f() { return \"<@U123>\" }\n```",
			want: "```\nfunc f() { return \"<@U123>\" }\n```",
		},
		{
			name: "inline code span with html-escaped entities decoded, markup not rewritten",
			in:   "fix `if (a &lt; b) { return &amp;result; }` please",
			want: "fix `if (a < b) { return &result; }` please",
		},
		{
			name: "fenced code block with html-escaped entities decoded, markup not rewritten",
			in:   "```\nif a &gt; b &amp;&amp; c &lt; d {\n  *not bold* <@U123>\n}\n```",
			want: "```\nif a > b && c < d {\n  *not bold* <@U123>\n}\n```",
		},
		{
			name: "combined mention, link, and bold",
			in:   "<@U0LAN0Z89> please review <https://example.com/pr|PR #42> -- *urgent*",
			want: "@U0LAN0Z89 please review PR #42 (https://example.com/pr) -- **urgent**",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeMrkdwn(tc.in)
			if got != tc.want {
				t.Errorf("normalizeMrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
