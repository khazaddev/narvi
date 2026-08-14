package github

import (
	"context"
	"testing"

	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
)

const arIssueCommentPayload = `{
	"action": "created",
	"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
	"comment": {"id": 666111, "body": "arch recap wrong: the rejected alternative was never actually considered", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const arIssueCommentOnPlainIssuePayload = `{
	"action": "created",
	"issue": {"number": 42},
	"comment": {"id": 666112, "body": "arch recap wrong: not on a PR", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const arIssueCommentEditedPayload = `{
	"action": "edited",
	"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
	"comment": {"id": 666113, "body": "arch recap wrong: edited, not created", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const arOrdinaryMentionPayload = `{
	"action": "created",
	"issue": {"number": 42, "pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}},
	"comment": {"id": 666114, "body": "@narvi-bot please take another look", "user": {"id": 1001, "login": "maintainer1"}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

const arReviewCommentPayload = `{
	"action": "created",
	"comment": {"id": 666115, "body": "arch recap wrong: missed the real decision", "user": {"id": 1002, "login": "maintainer2"}},
	"pull_request": {"number": 7, "head": {"ref": "feature-x", "sha": "abc123", "repo": {"name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}}},
	"repository": {"full_name": "acme/widgets", "name": "widgets", "clone_url": "https://github.com/acme/widgets.git"}
}`

func TestParseArchRecapContestCandidate_IssueCommentOnPR(t *testing.T) {
	t.Parallel()

	got, ok, err := parseArchRecapContestCandidate(eventTypeIssueComment, []byte(arIssueCommentPayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true for a created comment on a PR")
	}
	if got.RepoFullName != "acme/widgets" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/widgets")
	}
	if got.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want %d", got.PRNumber, 42)
	}
	if got.CommentID != 666111 {
		t.Errorf("CommentID = %d, want %d", got.CommentID, 666111)
	}
	if got.CommenterID != 1001 || got.CommenterLogin != "maintainer1" {
		t.Errorf("CommenterID/Login = %d/%q, want 1001/maintainer1", got.CommenterID, got.CommenterLogin)
	}
	if got.Body != "arch recap wrong: the rejected alternative was never actually considered" {
		t.Errorf("Body = %q, unexpected", got.Body)
	}
}

func TestParseArchRecapContestCandidate_PlainIssueRejected(t *testing.T) {
	t.Parallel()

	_, ok, err := parseArchRecapContestCandidate(eventTypeIssueComment, []byte(arIssueCommentOnPlainIssuePayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("ok = true, want false for a comment on a plain issue (no pull_request)")
	}
}

func TestParseArchRecapContestCandidate_NonCreatedActionRejected(t *testing.T) {
	t.Parallel()

	_, ok, err := parseArchRecapContestCandidate(eventTypeIssueComment, []byte(arIssueCommentEditedPayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("ok = true, want false for an \"edited\" action")
	}
}

func TestParseArchRecapContestCandidate_PullRequestReviewComment(t *testing.T) {
	t.Parallel()

	got, ok, err := parseArchRecapContestCandidate(eventTypePullRequestReviewComment, []byte(arReviewCommentPayload))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want %d", got.PRNumber, 7)
	}
	if got.CommentID != 666115 {
		t.Errorf("CommentID = %d, want %d", got.CommentID, 666115)
	}
	if got.RepoFullName != "acme/widgets" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/widgets")
	}
}

func TestParseArchRecapContestCandidate_UnrecognizedEventType(t *testing.T) {
	t.Parallel()

	_, ok, err := parseArchRecapContestCandidate("pull_request", []byte(`{}`))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("ok = true, want false for an event type this function doesn't handle at all")
	}
}

func TestParseArchRecapContestCandidate_MalformedJSON(t *testing.T) {
	t.Parallel()

	_, ok, err := parseArchRecapContestCandidate(eventTypeIssueComment, []byte(`not json`))
	if err == nil {
		t.Fatal("err = nil, want a decode error")
	}
	if ok {
		t.Error("ok = true, want false on a decode error")
	}
}

// TestTryCaptureArchRecapContest_NotApplicable_NeverTouchesDependencies
// mirrors TestTryCaptureFalsePositivePattern_NotApplicable_NeverTouchesDependencies's
// own identical precedent (falsepositivecapture_test.go): a delivery which
// is NOT a contest command short-circuits to archRecapContestNotApplicable
// WITHOUT ever touching identities/users/auditLog/reviewVerdicts/feedback
// -- passed as nil/zero here, so a regression that touches any of them on
// this path would panic, exactly the signal this test wants.
func TestTryCaptureArchRecapContest_NotApplicable_NeverTouchesDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		body      []byte
	}{
		{"ordinary mention, no arch-recap-wrong prefix", eventTypeIssueComment, []byte(arOrdinaryMentionPayload)},
		{"comment on a plain issue, not a PR", eventTypeIssueComment, []byte(arIssueCommentOnPlainIssuePayload)},
		{"non-created action", eventTypeIssueComment, []byte(arIssueCommentEditedPayload)},
		{"unrecognized event type", "pull_request", []byte(`{}`)},
		{"malformed JSON", eventTypeIssueComment, []byte(`not json`)},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tryCaptureArchRecapContest(context.Background(), fpDiscardLogger(), nil, nil, nil, appreviewverdict.Deps{}, nil, tc.eventType, tc.body)
			if got != archRecapContestNotApplicable {
				t.Errorf("outcome = %v, want archRecapContestNotApplicable", got)
			}
		})
	}
}
