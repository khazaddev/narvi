package releasereview_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/releasereview"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/platform"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeMergedPRLister is a test-only releasereview.MergedPRLister -- no
// real HTTP round trip, mirroring internal/app/reviewcontext's own
// fakeFetcher precedent exactly.
type fakeMergedPRLister struct {
	merged  []ports.MergedPR
	err     error
	calls   int
	lastReq ports.ListMergedBetweenSpec
}

func (f *fakeMergedPRLister) ListMergedBetween(_ context.Context, spec ports.ListMergedBetweenSpec) ([]ports.MergedPR, error) {
	f.calls++
	f.lastReq = spec
	return f.merged, f.err
}

// fakeOutboxEnqueuer is a test-only releasereview.OutboxEnqueuer.
type fakeOutboxEnqueuer struct {
	err        error
	calls      int
	lastParams sqlcgen.CreateOutboxEntryParams
}

func (f *fakeOutboxEnqueuer) Create(_ context.Context, arg sqlcgen.CreateOutboxEntryParams) (sqlcgen.Outbox, error) {
	f.calls++
	f.lastParams = arg
	if f.err != nil {
		return sqlcgen.Outbox{}, f.err
	}
	return sqlcgen.Outbox{ID: arg.SessionID}, nil
}

func testSessionID(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan("11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("scan test session id: %v", err)
	}
	return id
}

// TestRun_CleanManifest_EnqueuesReleaseManifestOutboxRow proves the happy
// path: ListMergedBetween is called with the right spec, and exactly one
// ports.NotificationKindReleaseManifest outbox row is enqueued, scoped to
// the given SessionID, carrying a rendered comment.
func TestRun_CleanManifest_EnqueuesReleaseManifestOutboxRow(t *testing.T) {
	t.Parallel()

	sessionID := testSessionID(t)
	lister := &fakeMergedPRLister{merged: []ports.MergedPR{
		{Number: 1, Title: "a", HasApprovingReview: true, CIConclusionAtMergeSHA: ports.CIConclusionSuccess},
	}}
	outbox := &fakeOutboxEnqueuer{}

	releasereview.Run(context.Background(), discardLogger(), releasereview.Deps{
		SourceControl: lister,
		Outbox:        outbox,
		Timeouts:      platform.DefaultTimeouts(),
	}, releasereview.Input{
		SessionID: sessionID,
		Owner:     "acme",
		Repo:      "widgets",
		PRNumber:  99,
		BaseRef:   "main",
		HeadRef:   "release/1.0",
		Token:     "gho_bottoken",
	})

	if lister.calls != 1 {
		t.Fatalf("ListMergedBetween calls = %d, want 1", lister.calls)
	}
	if lister.lastReq.Owner != "acme" || lister.lastReq.Repo != "widgets" ||
		lister.lastReq.BaseRef != "main" || lister.lastReq.HeadRef != "release/1.0" || lister.lastReq.Token != "gho_bottoken" {
		t.Errorf("ListMergedBetween spec = %+v, want owner=acme repo=widgets base=main head=release/1.0 token=gho_bottoken", lister.lastReq)
	}

	if outbox.calls != 1 {
		t.Fatalf("Outbox.Create calls = %d, want 1", outbox.calls)
	}
	if outbox.lastParams.SessionID != sessionID {
		t.Errorf("outbox row SessionID = %v, want %v", outbox.lastParams.SessionID, sessionID)
	}
	if outbox.lastParams.Kind != string(ports.NotificationKindReleaseManifest) {
		t.Errorf("outbox row Kind = %q, want %q", outbox.lastParams.Kind, ports.NotificationKindReleaseManifest)
	}

	var payload githubapi.ReleaseManifestPayload
	if err := json.Unmarshal(outbox.lastParams.Payload, &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if payload.Owner != "acme" || payload.Repo != "widgets" || payload.PRNumber != 99 {
		t.Errorf("payload = %+v, want owner=acme repo=widgets pr_number=99", payload)
	}
	if payload.Body == "" {
		t.Error("payload.Body is empty, want a rendered manifest comment")
	}
}

// TestRun_ListMergedBetweenFails_NeverEnqueues proves a failed
// ListMergedBetween call is best-effort: logged (implicitly, via the
// discard logger) and this function simply returns without ever calling
// Outbox.Create.
func TestRun_ListMergedBetweenFails_NeverEnqueues(t *testing.T) {
	t.Parallel()

	lister := &fakeMergedPRLister{err: errors.New("network exploded")}
	outbox := &fakeOutboxEnqueuer{}

	releasereview.Run(context.Background(), discardLogger(), releasereview.Deps{
		SourceControl: lister,
		Outbox:        outbox,
		Timeouts:      platform.DefaultTimeouts(),
	}, releasereview.Input{
		SessionID: testSessionID(t),
		Owner:     "acme",
		Repo:      "widgets",
		PRNumber:  99,
		BaseRef:   "main",
		HeadRef:   "release/1.0",
		Token:     "gho_bottoken",
	})

	if outbox.calls != 0 {
		t.Errorf("Outbox.Create calls = %d, want 0 (ListMergedBetween failed, nothing to post)", outbox.calls)
	}
}

// TestRun_OutboxCreateFails_NeverPanics proves a failed outbox enqueue is
// also best-effort -- logged, never propagated (Run has no error return
// at all).
func TestRun_OutboxCreateFails_NeverPanics(t *testing.T) {
	t.Parallel()

	lister := &fakeMergedPRLister{}
	outbox := &fakeOutboxEnqueuer{err: errors.New("db exploded")}

	releasereview.Run(context.Background(), discardLogger(), releasereview.Deps{
		SourceControl: lister,
		Outbox:        outbox,
		Timeouts:      platform.DefaultTimeouts(),
	}, releasereview.Input{
		SessionID: testSessionID(t),
		Owner:     "acme",
		Repo:      "widgets",
		PRNumber:  99,
		BaseRef:   "main",
		HeadRef:   "release/1.0",
		Token:     "gho_bottoken",
	})
	// Reaching here without panicking is the assertion.
	if outbox.calls != 1 {
		t.Errorf("Outbox.Create calls = %d, want 1 (it was attempted, even though it failed)", outbox.calls)
	}
}

// TestRun_HighRiskLabelDerivesHighRiskFlagged proves the boundary
// conversion (ports.MergedPR.Labels -> review.MergedPR.HighRiskFlagged)
// actually reads reviewpost.LabelHighRisk -- exercised indirectly via the
// aggregate-review signal reaching the rendered comment (the only
// externally-observable trace of the conversion from outside this
// package).
func TestRun_HighRiskLabelDerivesHighRiskFlagged(t *testing.T) {
	t.Parallel()

	lister := &fakeMergedPRLister{merged: []ports.MergedPR{
		{Number: 1, Title: "a", HasApprovingReview: true, Labels: []string{reviewpost.LabelHighRisk}},
	}}
	outbox := &fakeOutboxEnqueuer{}

	releasereview.Run(context.Background(), discardLogger(), releasereview.Deps{
		SourceControl: lister,
		Outbox:        outbox,
		Timeouts:      platform.DefaultTimeouts(),
	}, releasereview.Input{
		SessionID: testSessionID(t),
		Owner:     "acme", Repo: "widgets", PRNumber: 1, BaseRef: "main", HeadRef: "release/1.0", Token: "t",
	})

	var payload githubapi.ReleaseManifestPayload
	if err := json.Unmarshal(outbox.lastParams.Payload, &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if !strings.Contains(payload.Body, "aggregate diff review") {
		t.Errorf("rendered comment does not mention the aggregate review trigger, want it (HighRiskFlagged should have triggered §15.3) -- body:\n%s", payload.Body)
	}
}

// TestRun_RevertTimingConvertedFromTimestamps proves toDomainMergedPR's
// own RevertedAt-minus-MergedAt duration conversion reaches the rendered
// comment.
func TestRun_RevertTimingConvertedFromTimestamps(t *testing.T) {
	t.Parallel()

	mergedAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	revertedAt := mergedAt.Add(2 * time.Hour)
	lister := &fakeMergedPRLister{merged: []ports.MergedPR{
		{
			Number: 160, Title: "risky",
			HasApprovingReview: true,
			MergedAt:           mergedAt,
			WasReverted:        true,
			RevertedAt:         &revertedAt,
			RevertReviewed:     false,
		},
	}}
	outbox := &fakeOutboxEnqueuer{}

	releasereview.Run(context.Background(), discardLogger(), releasereview.Deps{
		SourceControl: lister,
		Outbox:        outbox,
		Timeouts:      platform.DefaultTimeouts(),
	}, releasereview.Input{
		SessionID: testSessionID(t),
		Owner:     "acme", Repo: "widgets", PRNumber: 1, BaseRef: "main", HeadRef: "release/1.0", Token: "t",
	})

	var payload githubapi.ReleaseManifestPayload
	if err := json.Unmarshal(outbox.lastParams.Payload, &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if !strings.Contains(payload.Body, "reverted 2h after merge") {
		t.Errorf("rendered comment missing the expected '2h' revert timing -- body:\n%s", payload.Body)
	}
}
