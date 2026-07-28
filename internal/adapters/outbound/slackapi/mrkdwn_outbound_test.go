package slackapi_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
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
			in:   "### Step 2: migrate the schema",
			want: "*Step 2: migrate the schema*",
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
