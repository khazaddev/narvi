package github

import (
	"testing"
)

func TestCompileMentionPattern(t *testing.T) {
	tests := []struct {
		name      string
		botHandle string
		body      string
		want      bool
	}{
		{name: "exact mention matches", botHandle: "narvi-bot", body: "@narvi-bot please review", want: true},
		{name: "case-insensitive matches", botHandle: "narvi-bot", body: "hey @Narvi-Bot take a look", want: true},
		{name: "mid-sentence matches", botHandle: "narvi-bot", body: "cc @narvi-bot, thanks!", want: true},
		{name: "no mention at all", botHandle: "narvi-bot", body: "please review this PR", want: false},
		{name: "different handle does not match", botHandle: "narvi-bot", body: "@some-other-bot review this", want: false},
		{name: "longer handle superstring rejected by word boundary", botHandle: "narvi", body: "@narvi-bot-2 review this", want: false},
		{name: "email-shaped string rejected", botHandle: "narvi-bot", body: "contact user@narvi-bot.example for help", want: false},
		{name: "handle at start of body matches", botHandle: "narvi-bot", body: "@narvi-bot hello", want: true},
		{name: "team mention sharing handle as prefix rejected", botHandle: "narvi", body: "please cc @narvi/maintainers for review", want: false},
		{name: "team mention with dash slug sharing handle as prefix rejected", botHandle: "narvi", body: "@narvi/team-x take a look", want: false},
		{name: "dotted suffix sharing handle as prefix rejected", botHandle: "narvi", body: "cc @narvi.bot for triage", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			re := compileMentionPattern(tc.botHandle)
			got := re.MatchString(tc.body)
			if got != tc.want {
				t.Errorf("compileMentionPattern(%q).MatchString(%q) = %v, want %v", tc.botHandle, tc.body, got, tc.want)
			}
		})
	}
}

const testBotHandle = "narvi-bot"

func TestParseIssueComment(t *testing.T) {
	mentionRE := compileMentionPattern(testBotHandle)

	tests := []struct {
		name    string
		body    string
		wantOK  bool
		wantErr bool
		check   func(t *testing.T, m mention)
	}{
		{
			name: "PR comment mentioning bot, action created -- actionable",
			body: `{
				"action": "created",
				"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
				"comment": {"body": "@narvi-bot please review"},
				"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
			}`,
			wantOK: true,
			check: func(t *testing.T, m mention) {
				if m.RepoFullName != "acme/widgets" {
					t.Errorf("RepoFullName = %q, want %q", m.RepoFullName, "acme/widgets")
				}
				if m.RepoName != "widgets" {
					t.Errorf("RepoName = %q, want %q", m.RepoName, "widgets")
				}
				if m.RepoCloneURL != "https://github.com/acme/widgets.git" {
					t.Errorf("RepoCloneURL = %q, want %q", m.RepoCloneURL, "https://github.com/acme/widgets.git")
				}
				if m.PRNumber != 42 {
					t.Errorf("PRNumber = %d, want 42", m.PRNumber)
				}
				if m.HeadBranch != nil {
					t.Errorf("HeadBranch = %v, want nil (issue_comment carries no head ref)", *m.HeadBranch)
				}
				if m.CommentBody != "@narvi-bot please review" {
					t.Errorf("CommentBody = %q, want %q", m.CommentBody, "@narvi-bot please review")
				}
			},
		},
		{
			name: "plain issue comment (not a PR) -- ignored",
			body: `{
				"action": "created",
				"issue": {"number": 7},
				"comment": {"body": "@narvi-bot please review"},
				"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
			}`,
			wantOK: false,
		},
		{
			name: "PR comment not mentioning bot -- ignored",
			body: `{
				"action": "created",
				"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
				"comment": {"body": "looks good to me"},
				"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
			}`,
			wantOK: false,
		},
		{
			name: "edited action -- ignored",
			body: `{
				"action": "edited",
				"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
				"comment": {"body": "@narvi-bot please review"},
				"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
			}`,
			wantOK: false,
		},
		{
			name:    "malformed JSON -- error",
			body:    `{not json`,
			wantOK:  false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok, err := parseIssueComment([]byte(tc.body), mentionRE)
			if tc.wantErr {
				if err == nil {
					t.Fatal("parseIssueComment() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIssueComment() error = %v, want nil", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("parseIssueComment() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && tc.check != nil {
				tc.check(t, m)
			}
		})
	}
}

func TestParsePullRequestReviewComment(t *testing.T) {
	mentionRE := compileMentionPattern(testBotHandle)

	tests := []struct {
		name    string
		body    string
		wantOK  bool
		wantErr bool
		check   func(t *testing.T, m mention)
	}{
		{
			name: "review comment mentioning bot on a fork PR -- actionable, head repo used for clone",
			body: `{
				"action": "created",
				"comment": {"body": "@narvi-bot what do you think of this line?"},
				"pull_request": {
					"number": 99,
					"head": {"ref": "feature-x", "repo": {"name": "widgets", "clone_url": "https://github.com/contributor/widgets.git"}}
				},
				"repository": {"full_name": "acme/widgets"}
			}`,
			wantOK: true,
			check: func(t *testing.T, m mention) {
				if m.RepoFullName != "acme/widgets" {
					t.Errorf("RepoFullName = %q, want %q (base repo, the claim key)", m.RepoFullName, "acme/widgets")
				}
				if m.RepoCloneURL != "https://github.com/contributor/widgets.git" {
					t.Errorf("RepoCloneURL = %q, want the HEAD (fork) repo's own clone url", m.RepoCloneURL)
				}
				if m.PRNumber != 99 {
					t.Errorf("PRNumber = %d, want 99", m.PRNumber)
				}
				if m.HeadBranch == nil || *m.HeadBranch != "feature-x" {
					t.Errorf("HeadBranch = %v, want %q", m.HeadBranch, "feature-x")
				}
			},
		},
		{
			name: "no mention -- ignored",
			body: `{
				"action": "created",
				"comment": {"body": "nice catch"},
				"pull_request": {"number": 99, "head": {"ref": "feature-x", "repo": {"name": "widgets", "clone_url": "https://github.com/contributor/widgets.git"}}},
				"repository": {"full_name": "acme/widgets"}
			}`,
			wantOK: false,
		},
		{
			name: "deleted action -- ignored",
			body: `{
				"action": "deleted",
				"comment": {"body": "@narvi-bot look again"},
				"pull_request": {"number": 99, "head": {"ref": "feature-x", "repo": {"name": "widgets", "clone_url": "https://github.com/contributor/widgets.git"}}},
				"repository": {"full_name": "acme/widgets"}
			}`,
			wantOK: false,
		},
		{
			name:    "malformed JSON -- error",
			body:    `{not json`,
			wantOK:  false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok, err := parsePullRequestReviewComment([]byte(tc.body), mentionRE)
			if tc.wantErr {
				if err == nil {
					t.Fatal("parsePullRequestReviewComment() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePullRequestReviewComment() error = %v, want nil", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("parsePullRequestReviewComment() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && tc.check != nil {
				tc.check(t, m)
			}
		})
	}
}

func TestParseMention_UnrecognizedEventType(t *testing.T) {
	mentionRE := compileMentionPattern(testBotHandle)
	_, ok, err := parseMention("pull_request", []byte(`{}`), mentionRE)
	if err != nil {
		t.Fatalf("parseMention() error = %v, want nil", err)
	}
	if ok {
		t.Error("parseMention() ok = true, want false for an event type this adapter doesn't act on")
	}
}

// TestParseMention_CommenterIdentity is batch fix/audit-github-actor-rbac's
// own table-driven test for the new comment.user.{id,login} parsing (the
// H4 audit finding this batch closes: GitHub ingress never even parsed WHO
// commented) -- table-driven over BOTH event shapes this package's own
// parseMention dispatches on, since GitHub uses the identical comment.user
// shape for each (payload.go's own doc comment on pullRequestReviewCommentPayload.
// Comment.User).
func TestParseMention_CommenterIdentity(t *testing.T) {
	mentionRE := compileMentionPattern(testBotHandle)

	tests := []struct {
		name               string
		eventType          string
		body               string
		wantCommenterID    int64
		wantCommenterLogin string
	}{
		{
			name:      "issue_comment carries commenter id/login",
			eventType: eventTypeIssueComment,
			body: `{
				"action": "created",
				"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
				"comment": {"body": "@narvi-bot please review", "user": {"id": 555111, "login": "octo-reviewer"}},
				"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
			}`,
			wantCommenterID:    555111,
			wantCommenterLogin: "octo-reviewer",
		},
		{
			name:      "issue_comment with no comment.user -- zero-valued, never an error",
			eventType: eventTypeIssueComment,
			body: `{
				"action": "created",
				"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
				"comment": {"body": "@narvi-bot please review"},
				"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
			}`,
			wantCommenterID:    0,
			wantCommenterLogin: "",
		},
		{
			name:      "pull_request_review_comment carries commenter id/login",
			eventType: eventTypePullRequestReviewComment,
			body: `{
				"action": "created",
				"comment": {"body": "@narvi-bot what do you think?", "user": {"id": 777222, "login": "line-commenter"}},
				"pull_request": {
					"number": 99,
					"head": {"ref": "feature-x", "repo": {"name": "widgets", "clone_url": "https://github.com/contributor/widgets.git"}}
				},
				"repository": {"full_name": "acme/widgets"}
			}`,
			wantCommenterID:    777222,
			wantCommenterLogin: "line-commenter",
		},
		{
			name:      "pull_request_review_comment with no comment.user -- zero-valued, never an error",
			eventType: eventTypePullRequestReviewComment,
			body: `{
				"action": "created",
				"comment": {"body": "@narvi-bot what do you think?"},
				"pull_request": {
					"number": 99,
					"head": {"ref": "feature-x", "repo": {"name": "widgets", "clone_url": "https://github.com/contributor/widgets.git"}}
				},
				"repository": {"full_name": "acme/widgets"}
			}`,
			wantCommenterID:    0,
			wantCommenterLogin: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok, err := parseMention(tc.eventType, []byte(tc.body), mentionRE)
			if err != nil {
				t.Fatalf("parseMention() error = %v, want nil", err)
			}
			if !ok {
				t.Fatal("parseMention() ok = false, want true (a genuine, actionable mention)")
			}
			if m.CommenterID != tc.wantCommenterID {
				t.Errorf("CommenterID = %d, want %d", m.CommenterID, tc.wantCommenterID)
			}
			if m.CommenterLogin != tc.wantCommenterLogin {
				t.Errorf("CommenterLogin = %q, want %q", m.CommenterLogin, tc.wantCommenterLogin)
			}
		})
	}
}
