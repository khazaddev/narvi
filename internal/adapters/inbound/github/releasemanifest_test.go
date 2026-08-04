package github

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeMergedPRLister/fakeOutboxEnqueuer are test-only stand-ins for
// releasereview.MergedPRLister/OutboxEnqueuer -- mirrors this file's own
// sibling fakePullRequestResolver (headresolve_test.go) precedent
// exactly: no real HTTP/DB round trip.
type fakeMergedPRLister struct {
	merged []ports.MergedPR
	err    error
	calls  int
}

func (f *fakeMergedPRLister) ListMergedBetween(context.Context, ports.ListMergedBetweenSpec) ([]ports.MergedPR, error) {
	f.calls++
	return f.merged, f.err
}

type fakeOutboxEnqueuer struct {
	calls int
}

func (f *fakeOutboxEnqueuer) Create(_ context.Context, _ sqlcgen.CreateOutboxEntryParams) (sqlcgen.Outbox, error) {
	f.calls++
	return sqlcgen.Outbox{}, nil
}

func testSessionID(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan("11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("scan test session id: %v", err)
	}
	return id
}

func baseReleaseCfg(pr githubapi.PullRequest, lister *fakeMergedPRLister, outbox *fakeOutboxEnqueuer) Config {
	return Config{
		BotToken:             "gho_bottoken",
		PullRequests:         &fakePullRequestResolver{pr: pr},
		SourceControl:        lister,
		Outbox:               outbox,
		ReleaseLabel:         "release",
		ReleaseBranchPattern: "release/*",
		Timeouts:             platform.DefaultTimeouts(),
	}
}

// TestTriggerReleaseManifestCheck_ReleaseBranchDetected proves a PR whose
// head branch matches the configured release pattern triggers the
// manifest check end to end: ListMergedBetween is called, and exactly
// one outbox row is enqueued.
func TestTriggerReleaseManifestCheck_ReleaseBranchDetected(t *testing.T) {
	lister := &fakeMergedPRLister{merged: []ports.MergedPR{
		{Number: 1, Title: "a", HasApprovingReview: true},
	}}
	outbox := &fakeOutboxEnqueuer{}
	cfg := baseReleaseCfg(githubapi.PullRequest{HeadRef: "release/2.4", BaseRef: "main"}, lister, outbox)

	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "acme/widgets", 42, testSessionID(t))

	if lister.calls != 1 {
		t.Fatalf("ListMergedBetween calls = %d, want 1 (release PR should have been detected)", lister.calls)
	}
	if outbox.calls != 1 {
		t.Fatalf("Outbox.Create calls = %d, want 1", outbox.calls)
	}
}

// TestTriggerReleaseManifestCheck_ReleaseLabelDetected proves the label
// axis of §15.1's OR also triggers the check, independent of branch
// naming.
func TestTriggerReleaseManifestCheck_ReleaseLabelDetected(t *testing.T) {
	lister := &fakeMergedPRLister{}
	outbox := &fakeOutboxEnqueuer{}
	cfg := baseReleaseCfg(githubapi.PullRequest{HeadRef: "develop", BaseRef: "main", Labels: []string{"release"}}, lister, outbox)

	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "acme/widgets", 42, testSessionID(t))

	if lister.calls != 1 {
		t.Fatalf("ListMergedBetween calls = %d, want 1 (release label should have been detected)", lister.calls)
	}
}

// TestTriggerReleaseManifestCheck_OrdinaryPRNeverTriggers proves an
// ordinary PR (no release branch/label signal) never calls
// ListMergedBetween or enqueues anything at all.
func TestTriggerReleaseManifestCheck_OrdinaryPRNeverTriggers(t *testing.T) {
	lister := &fakeMergedPRLister{}
	outbox := &fakeOutboxEnqueuer{}
	cfg := baseReleaseCfg(githubapi.PullRequest{HeadRef: "feature/foo", BaseRef: "main"}, lister, outbox)

	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "acme/widgets", 42, testSessionID(t))

	if lister.calls != 0 {
		t.Errorf("ListMergedBetween calls = %d, want 0 (not a release PR)", lister.calls)
	}
	if outbox.calls != 0 {
		t.Errorf("Outbox.Create calls = %d, want 0 (not a release PR)", outbox.calls)
	}
}

// TestTriggerReleaseManifestCheck_NilDepsSkipsEntirely proves a nil
// SourceControl or Outbox (this package's own handler_test.go, or any
// other minimal wiring) skips this function entirely -- no GetPullRequest
// call even attempted, mirroring cfg.DiffFetcher's own identical nil-safe
// precedent.
func TestTriggerReleaseManifestCheck_NilDepsSkipsEntirely(t *testing.T) {
	resolver := &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: "release/2.4"}}
	cfg := Config{
		BotToken:     "gho_bottoken",
		PullRequests: resolver,
		Timeouts:     platform.DefaultTimeouts(),
		// SourceControl/Outbox deliberately left nil.
	}

	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "acme/widgets", 42, testSessionID(t))

	if resolver.calls != 0 {
		t.Errorf("GetPullRequest calls = %d, want 0 (nil SourceControl/Outbox must skip before even fetching the PR)", resolver.calls)
	}
}

// TestTriggerReleaseManifestCheck_GetPullRequestFailsNeverPanics proves a
// failed GetPullRequest call (detection's own fresh fetch) degrades to
// "skip this PR", never a panic or a ListMergedBetween call with garbage
// data.
func TestTriggerReleaseManifestCheck_GetPullRequestFailsNeverPanics(t *testing.T) {
	lister := &fakeMergedPRLister{}
	outbox := &fakeOutboxEnqueuer{}
	cfg := Config{
		BotToken:             "gho_bottoken",
		PullRequests:         &fakePullRequestResolver{err: errors.New("network exploded")},
		SourceControl:        lister,
		Outbox:               outbox,
		ReleaseLabel:         "release",
		ReleaseBranchPattern: "release/*",
		Timeouts:             platform.DefaultTimeouts(),
	}

	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "acme/widgets", 42, testSessionID(t))

	if lister.calls != 0 {
		t.Errorf("ListMergedBetween calls = %d, want 0 (detection fetch itself failed)", lister.calls)
	}
}

// TestTriggerReleaseManifestCheck_UnsplittableRepoFullNameSkips proves a
// defensively-handled malformed repoFullName (no "/") never panics or
// calls GetPullRequest with garbage owner/repo.
func TestTriggerReleaseManifestCheck_UnsplittableRepoFullNameSkips(t *testing.T) {
	resolver := &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: "release/2.4"}}
	lister := &fakeMergedPRLister{}
	outbox := &fakeOutboxEnqueuer{}
	cfg := Config{
		BotToken:             "gho_bottoken",
		PullRequests:         resolver,
		SourceControl:        lister,
		Outbox:               outbox,
		ReleaseLabel:         "release",
		ReleaseBranchPattern: "release/*",
		Timeouts:             platform.DefaultTimeouts(),
	}

	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "not-a-valid-full-name", 42, testSessionID(t))

	if resolver.calls != 0 {
		t.Errorf("GetPullRequest calls = %d, want 0 (unsplittable repo_full_name)", resolver.calls)
	}
}

// TestTriggerReleaseManifestCheck_PayloadCarriesSessionAndPRIdentity
// proves the enqueued outbox payload correctly identifies the release
// PR -- a regression here would silently post a manifest comment onto
// the WRONG pull request.
func TestTriggerReleaseManifestCheck_PayloadCarriesSessionAndPRIdentity(t *testing.T) {
	lister := &fakeMergedPRLister{merged: []ports.MergedPR{{Number: 1, HasApprovingReview: true}}}

	var gotParams sqlcgen.CreateOutboxEntryParams
	captured := captureOutboxEnqueuer{capture: &gotParams}
	cfg := baseReleaseCfg(githubapi.PullRequest{HeadRef: "release/2.4", BaseRef: "main"}, lister, nil)
	cfg.Outbox = &captured

	sessionID := testSessionID(t)
	triggerReleaseManifestCheckBestEffort(context.Background(), discardLogger(), cfg, "acme/widgets", 42, sessionID)

	if gotParams.SessionID != sessionID {
		t.Errorf("outbox row SessionID = %v, want %v", gotParams.SessionID, sessionID)
	}
	if gotParams.Kind != string(ports.NotificationKindReleaseManifest) {
		t.Errorf("outbox row Kind = %q, want %q", gotParams.Kind, ports.NotificationKindReleaseManifest)
	}

	var payload githubapi.ReleaseManifestPayload
	if err := json.Unmarshal(gotParams.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Owner != "acme" || payload.Repo != "widgets" || payload.PRNumber != 42 {
		t.Errorf("payload identity = %+v, want owner=acme repo=widgets pr_number=42", payload)
	}
}

// captureOutboxEnqueuer records the exact params it was called with, for
// TestTriggerReleaseManifestCheck_PayloadCarriesSessionAndPRIdentity.
type captureOutboxEnqueuer struct {
	capture *sqlcgen.CreateOutboxEntryParams
}

func (c *captureOutboxEnqueuer) Create(_ context.Context, arg sqlcgen.CreateOutboxEntryParams) (sqlcgen.Outbox, error) {
	*c.capture = arg
	return sqlcgen.Outbox{}, nil
}
