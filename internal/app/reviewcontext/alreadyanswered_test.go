package reviewcontext_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/reviewcontext"
)

// fakeFindingsFetcher is a test-only reviewcontext.FindingsFetcher -- no
// real Postgres connection, mirroring fakeFalsePositiveFetcher's own
// established local-fake precedent in this same test package.
type fakeFindingsFetcher struct {
	rows    []sqlcgen.ReviewFinding
	listErr error
}

func (f *fakeFindingsFetcher) ListOpenAndRebutted(_ context.Context, _ string, _ int32) ([]sqlcgen.ReviewFinding, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func TestFetchAlreadyAnswered_NilFetcher_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	got := reviewcontext.FetchAlreadyAnswered(context.Background(), discardLogger(), nil, "acme/widgets", 7, []string{"a.go"}, false)
	if got != "" {
		t.Errorf("FetchAlreadyAnswered() = %q, want empty for a nil fetcher", got)
	}
}

func TestFetchAlreadyAnswered_FetchErrorDegradesToEmpty(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFindingsFetcher{listErr: errors.New("db exploded")}
	got := reviewcontext.FetchAlreadyAnswered(context.Background(), discardLogger(), fetcher, "acme/widgets", 7, []string{"a.go"}, false)
	if got != "" {
		t.Errorf("FetchAlreadyAnswered() = %q, want empty on a fetch error", got)
	}
}

// TestFetchAlreadyAnswered_ThreadsChangedPathsIntoRetirement is this
// Step's own §22.1.2 refinement: FetchAlreadyAnswered forwards its
// changedPaths parameter, unmodified, through to reviewpost.
// RenderAlreadyAnsweredFacts, so a finding whose file has left the
// current diff renders RETIRED end-to-end from a Postgres row through
// this function's own output -- not just at reviewpost's own unit-test
// layer.
func TestFetchAlreadyAnswered_ThreadsChangedPathsIntoRetirement(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFindingsFetcher{
		rows: []sqlcgen.ReviewFinding{
			{IdentityHash: "abc123", FilePath: "internal/stale.go", Description: "Old finding on a file that left the diff.", Status: "open"},
			{IdentityHash: "def456", FilePath: "internal/live.go", Description: "Finding on a file still in the diff.", Status: "open"},
		},
	}

	got := reviewcontext.FetchAlreadyAnswered(context.Background(), discardLogger(), fetcher, "acme/widgets", 7, []string{"internal/live.go"}, false)

	if !strings.Contains(got, "internal/stale.go") || !strings.Contains(got, "internal/live.go") {
		t.Fatalf("FetchAlreadyAnswered() dropped a finding instead of noting retirement; got:\n%s", got)
	}

	var staleLine, liveLine string
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(line, "internal/stale.go"):
			staleLine = line
		case strings.Contains(line, "internal/live.go"):
			liveLine = line
		}
	}
	if staleLine == "" || liveLine == "" {
		t.Fatalf("FetchAlreadyAnswered() could not locate both findings' own lines; got:\n%s", got)
	}
	if !strings.Contains(staleLine, "RETIRED:") {
		t.Errorf("FetchAlreadyAnswered() did not mark the out-of-diff finding RETIRED; line:\n%s", staleLine)
	}
	if strings.Contains(liveLine, "RETIRED:") {
		t.Errorf("FetchAlreadyAnswered() wrongly marked the still-in-diff finding RETIRED; line:\n%s", liveLine)
	}
}

// TestFetchAlreadyAnswered_NilChangedPathsNeverRetires is the fail-safe
// direction (mirrors reviewpost.RenderAlreadyAnsweredFacts' own identical
// unit test): a caller with no reliable diff data (a nil/never-populated
// changedPaths, e.g. a failed upstream diff fetch) must never cause every
// carried finding to read as retired.
func TestFetchAlreadyAnswered_NilChangedPathsNeverRetires(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFindingsFetcher{
		rows: []sqlcgen.ReviewFinding{
			{IdentityHash: "abc123", FilePath: "internal/whatever.go", Description: "Some finding.", Status: "open"},
		},
	}

	got := reviewcontext.FetchAlreadyAnswered(context.Background(), discardLogger(), fetcher, "acme/widgets", 7, nil, false)
	if strings.Contains(got, "RETIRED:") {
		t.Errorf("FetchAlreadyAnswered() = %q, want no RETIRED marker when changedPaths is nil (no reliable diff data)", got)
	}
}

// TestFetchAlreadyAnswered_DiffTruncatedNeverRetires is D1's own SAFE-
// direction test end-to-end (adversarial review of PR #182, BLOCKING):
// even when changedPaths is a non-nil, non-empty list (the truncated
// diff's own genuine byte PREFIX), a caller that also reports
// diffTruncated=true must never let a finding whose file simply sorted
// past the fetch's own size cut render RETIRED -- forwarded, unmodified,
// through to reviewpost.RenderAlreadyAnsweredFacts.
func TestFetchAlreadyAnswered_DiffTruncatedNeverRetires(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFindingsFetcher{
		rows: []sqlcgen.ReviewFinding{
			{IdentityHash: "abc123", FilePath: "internal/auth/token.go", Description: "Live finding on a file past the truncation cut.", Status: "open"},
		},
	}

	// changedPaths reflects only the truncated diff's own captured
	// prefix -- internal/auth/token.go is genuinely still changed, it
	// simply never made it into this partial list.
	got := reviewcontext.FetchAlreadyAnswered(context.Background(), discardLogger(), fetcher, "acme/widgets", 7, []string{"vendor/aaa_padding_asset.bin"}, true)
	if strings.Contains(got, "RETIRED:") {
		t.Errorf("FetchAlreadyAnswered() = %q, want no RETIRED marker when diffTruncated is true", got)
	}
	if !strings.Contains(got, "internal/auth/token.go") {
		t.Errorf("FetchAlreadyAnswered() dropped the finding entirely instead of noting retirement was withheld: %q", got)
	}
}
