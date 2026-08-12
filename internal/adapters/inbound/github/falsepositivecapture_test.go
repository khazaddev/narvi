package github

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func fpDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const fpIssueCommentPayload = `{
	"action": "created",
	"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
	"comment": {"id": 555111, "body": "false positive: this logger call is intentionally unchecked", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const fpIssueCommentOnPlainIssuePayload = `{
	"action": "created",
	"issue": {"number": 42},
	"comment": {"id": 555112, "body": "false positive: not on a PR", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const fpIssueCommentEditedPayload = `{
	"action": "edited",
	"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
	"comment": {"id": 555113, "body": "false positive: edited, not created", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const fpOrdinaryMentionPayload = `{
	"action": "created",
	"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
	"comment": {"id": 555114, "body": "@narvi-bot please take another look", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const fpReviewCommentPayload = `{
	"action": "created",
	"comment": {"id": 555115, "body": "false positive: intentional in this diff", "user": {"id": 1002, "login": "maintainer2"}},
	"pull_request": {"number": 7, "head": {"ref": "feature-x", "sha": "abc123", "repo": {"name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

func TestParseFalsePositiveCandidate_IssueCommentOnPR(t *testing.T) {
	t.Parallel()

	got, ok, err := parseFalsePositiveCandidate(eventTypeIssueComment, []byte(fpIssueCommentPayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true for a created comment on a PR")
	}
	if got.RepoFullName != "acme/widgets" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/widgets")
	}
	if got.CommentID != 555111 {
		t.Errorf("CommentID = %d, want %d", got.CommentID, 555111)
	}
	if got.CommenterID != 1001 || got.CommenterLogin != "maintainer1" {
		t.Errorf("CommenterID/Login = %d/%q, want 1001/maintainer1", got.CommenterID, got.CommenterLogin)
	}
	if got.Body != "false positive: this logger call is intentionally unchecked" {
		t.Errorf("Body = %q, unexpected", got.Body)
	}
}

func TestParseFalsePositiveCandidate_PlainIssueRejected(t *testing.T) {
	t.Parallel()

	_, ok, err := parseFalsePositiveCandidate(eventTypeIssueComment, []byte(fpIssueCommentOnPlainIssuePayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("ok = true, want false for a comment on a plain issue (no pull_request)")
	}
}

func TestParseFalsePositiveCandidate_NonCreatedActionRejected(t *testing.T) {
	t.Parallel()

	_, ok, err := parseFalsePositiveCandidate(eventTypeIssueComment, []byte(fpIssueCommentEditedPayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("ok = true, want false for an \"edited\" action")
	}
}

func TestParseFalsePositiveCandidate_PullRequestReviewComment(t *testing.T) {
	t.Parallel()

	got, ok, err := parseFalsePositiveCandidate(eventTypePullRequestReviewComment, []byte(fpReviewCommentPayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.CommentID != 555115 {
		t.Errorf("CommentID = %d, want %d", got.CommentID, 555115)
	}
	if got.RepoFullName != "acme/widgets" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/widgets")
	}
}

func TestParseFalsePositiveCandidate_UnrecognizedEventType(t *testing.T) {
	t.Parallel()

	_, ok, err := parseFalsePositiveCandidate("pull_request", []byte(`{}`))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("ok = true, want false for an event type this function doesn't handle at all")
	}
}

func TestParseFalsePositiveCandidate_MalformedJSON(t *testing.T) {
	t.Parallel()

	_, ok, err := parseFalsePositiveCandidate(eventTypeIssueComment, []byte(`not json`))
	if err == nil {
		t.Fatal("err = nil, want a decode error")
	}
	if ok {
		t.Error("ok = true, want false on a decode error")
	}
}

// TestTryCaptureFalsePositivePattern_NotApplicable_NeverTouchesDependencies
// proves that a delivery which is NOT a capture command short-circuits to
// falsePositiveNotApplicable WITHOUT ever touching identities/users/
// auditLog/patterns -- passed as nil here, so a regression that touches
// any of them on this path would panic (a nil pointer dereference),
// exactly the signal this test wants.
func TestTryCaptureFalsePositivePattern_NotApplicable_NeverTouchesDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		body      []byte
	}{
		{"ordinary mention, no false-positive prefix", eventTypeIssueComment, []byte(fpOrdinaryMentionPayload)},
		{"comment on a plain issue, not a PR", eventTypeIssueComment, []byte(fpIssueCommentOnPlainIssuePayload)},
		{"non-created action", eventTypeIssueComment, []byte(fpIssueCommentEditedPayload)},
		{"unrecognized event type", "pull_request", []byte(`{}`)},
		{"malformed JSON", eventTypeIssueComment, []byte(`not json`)},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tryCaptureFalsePositivePattern(context.Background(), fpDiscardLogger(), nil, nil, nil, nil, tc.eventType, tc.body)
			if got != falsePositiveNotApplicable {
				t.Errorf("outcome = %v, want falsePositiveNotApplicable", got)
			}
		})
	}
}
