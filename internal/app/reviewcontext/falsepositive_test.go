package reviewcontext_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/reviewcontext"
)

// fakeFalsePositiveFetcher is a test-only
// reviewcontext.FalsePositivePatternsFetcher -- no real Postgres
// connection, mirroring fakeFetcher's own established local-fake
// precedent in this same test package.
type fakeFalsePositiveFetcher struct {
	rows    []sqlcgen.ReviewFalsePositivePattern
	listErr error

	incrementedIDs []pgtype.UUID
	incrementErr   error
	incrementCalls int
}

func (f *fakeFalsePositiveFetcher) ListActive(_ context.Context, _ string) ([]sqlcgen.ReviewFalsePositivePattern, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func (f *fakeFalsePositiveFetcher) IncrementHitCount(_ context.Context, ids []pgtype.UUID) error {
	f.incrementCalls++
	f.incrementedIDs = ids
	return f.incrementErr
}

func uuidFrom(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Bytes[0] = b
	u.Valid = true
	return u
}

func TestFetchFalsePositivePatterns_NilFetcher_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	got := reviewcontext.FetchFalsePositivePatterns(context.Background(), discardLogger(), nil, "acme/widgets")
	if got != "" {
		t.Errorf("FetchFalsePositivePatterns() = %q, want empty for a nil fetcher", got)
	}
}

func TestFetchFalsePositivePatterns_FetchErrorDegradesToEmpty(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFalsePositiveFetcher{listErr: errors.New("db exploded")}
	got := reviewcontext.FetchFalsePositivePatterns(context.Background(), discardLogger(), fetcher, "acme/widgets")
	if got != "" {
		t.Errorf("FetchFalsePositivePatterns() = %q, want empty on a fetch error (§22.4: a failed pattern read yields NO injected block)", got)
	}
	if fetcher.incrementCalls != 0 {
		t.Errorf("incrementCalls = %d, want 0 -- nothing to bump when the read itself failed", fetcher.incrementCalls)
	}
}

func TestFetchFalsePositivePatterns_NoActivePatterns_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFalsePositiveFetcher{rows: nil}
	got := reviewcontext.FetchFalsePositivePatterns(context.Background(), discardLogger(), fetcher, "acme/widgets")
	if got != "" {
		t.Errorf("FetchFalsePositivePatterns() = %q, want empty when this repo has no active patterns", got)
	}
}

func TestFetchFalsePositivePatterns_RendersEveryActivePattern(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFalsePositiveFetcher{
		rows: []sqlcgen.ReviewFalsePositivePattern{
			{ID: uuidFrom(1), Reason: "unchecked error on this logger call is intentional"},
			{ID: uuidFrom(2), Reason: "TODOs are tracked separately here"},
		},
	}

	got := reviewcontext.FetchFalsePositivePatterns(context.Background(), discardLogger(), fetcher, "acme/widgets")
	for _, want := range []string{"unchecked error on this logger call is intentional", "TODOs are tracked separately here"} {
		if !strings.Contains(got, want) {
			t.Errorf("FetchFalsePositivePatterns() missing %q; got:\n%s", want, got)
		}
	}
}

func TestFetchFalsePositivePatterns_IncrementsHitCountForEveryReturnedPattern(t *testing.T) {
	t.Parallel()

	id1, id2 := uuidFrom(1), uuidFrom(2)
	fetcher := &fakeFalsePositiveFetcher{
		rows: []sqlcgen.ReviewFalsePositivePattern{
			{ID: id1, Reason: "reason one"},
			{ID: id2, Reason: "reason two"},
		},
	}

	reviewcontext.FetchFalsePositivePatterns(context.Background(), discardLogger(), fetcher, "acme/widgets")

	if fetcher.incrementCalls != 1 {
		t.Fatalf("incrementCalls = %d, want 1 (one batched call covering every returned pattern)", fetcher.incrementCalls)
	}
	if len(fetcher.incrementedIDs) != 2 {
		t.Fatalf("incrementedIDs = %v, want 2 ids", fetcher.incrementedIDs)
	}
	got := map[pgtype.UUID]bool{fetcher.incrementedIDs[0]: true, fetcher.incrementedIDs[1]: true}
	if !got[id1] || !got[id2] {
		t.Errorf("incrementedIDs = %v, want both %v and %v present", fetcher.incrementedIDs, id1, id2)
	}
}

func TestFetchFalsePositivePatterns_IncrementFailureStillReturnsRenderedBlock(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFalsePositiveFetcher{
		rows:         []sqlcgen.ReviewFalsePositivePattern{{ID: uuidFrom(1), Reason: "reason one"}},
		incrementErr: errors.New("db exploded on the bookkeeping write"),
	}

	got := reviewcontext.FetchFalsePositivePatterns(context.Background(), discardLogger(), fetcher, "acme/widgets")
	if !strings.Contains(got, "reason one") {
		t.Errorf("FetchFalsePositivePatterns() = %q, want the rendered block even though the hit-count increment failed", got)
	}
}
