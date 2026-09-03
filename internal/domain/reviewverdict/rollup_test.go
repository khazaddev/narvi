package reviewverdict_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/review"
	"github.com/narvidev/narvi/internal/domain/reviewverdict"
)

func TestTimeseries_EmptyIsNotYetComputed(t *testing.T) {
	t.Parallel()

	buckets, ok := reviewverdict.Timeseries(nil)
	if ok {
		t.Fatalf("Timeseries(nil) ok = true, want false (not yet computed)")
	}
	if buckets != nil {
		t.Fatalf("Timeseries(nil) buckets = %v, want nil", buckets)
	}
}

func TestTimeseries_BucketsByUTCDayAndCountsShippable(t *testing.T) {
	t.Parallel()

	day1 := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	day1Later := time.Date(2026, 8, 1, 22, 0, 0, 0, time.UTC) // same UTC day, different hour
	day2 := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)

	records := []reviewverdict.Record{
		{CreatedAt: day1, Verdict: review.Verdict{Shippable: review.ShippableAuto}},
		{CreatedAt: day1Later, Verdict: review.Verdict{Shippable: review.ShippableAuto}},
		{CreatedAt: day1, Verdict: review.Verdict{Shippable: review.ShippableNeedsHuman}},
		{CreatedAt: day2, Verdict: review.Verdict{Shippable: review.ShippableBlock}},
	}

	buckets, ok := reviewverdict.Timeseries(records)
	if !ok {
		t.Fatalf("Timeseries(records) ok = false, want true")
	}
	if len(buckets) != 2 {
		t.Fatalf("Timeseries(records) = %d buckets, want 2 (one per calendar day)", len(buckets))
	}

	wantDay1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !buckets[0].Day.Equal(wantDay1) {
		t.Errorf("buckets[0].Day = %v, want %v", buckets[0].Day, wantDay1)
	}
	if got := buckets[0].Counts[review.ShippableAuto]; got != 2 {
		t.Errorf("buckets[0].Counts[auto] = %d, want 2", got)
	}
	if got := buckets[0].Counts[review.ShippableNeedsHuman]; got != 1 {
		t.Errorf("buckets[0].Counts[needs_human] = %d, want 1", got)
	}
	if got := buckets[0].Counts[review.ShippableBlock]; got != 0 {
		t.Errorf("buckets[0].Counts[block] = %d, want 0 (absent, not zero-valued present)", got)
	}

	wantDay2 := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if !buckets[1].Day.Equal(wantDay2) {
		t.Errorf("buckets[1].Day = %v, want %v", buckets[1].Day, wantDay2)
	}
	if got := buckets[1].Counts[review.ShippableBlock]; got != 1 {
		t.Errorf("buckets[1].Counts[block] = %d, want 1", got)
	}
}

func TestTopRiskDrivers_EmptyIsNotYetComputed(t *testing.T) {
	t.Parallel()

	drivers, ok := reviewverdict.TopRiskDrivers(nil)
	if ok {
		t.Fatalf("TopRiskDrivers(nil) ok = true, want false (not yet computed)")
	}
	if drivers != nil {
		t.Fatalf("TopRiskDrivers(nil) drivers = %v, want nil", drivers)
	}
}

func TestTopRiskDrivers_NonEmptyRecordsWithNoTagsIsARealZero(t *testing.T) {
	t.Parallel()

	records := []reviewverdict.Record{
		{Verdict: review.Verdict{BlastRadius: nil}},
	}
	drivers, ok := reviewverdict.TopRiskDrivers(records)
	if !ok {
		t.Fatalf("TopRiskDrivers(records) ok = false, want true (real data exists, it just tagged nothing)")
	}
	if len(drivers) != 0 {
		t.Fatalf("TopRiskDrivers(records) = %v, want empty", drivers)
	}
}

func TestTopRiskDrivers_CountsAndOrdersDeterministically(t *testing.T) {
	t.Parallel()

	records := []reviewverdict.Record{
		{Verdict: review.Verdict{BlastRadius: []review.Tag{review.TagAuth, review.TagMigrations}}},
		{Verdict: review.Verdict{BlastRadius: []review.Tag{review.TagAuth}}},
		{Verdict: review.Verdict{BlastRadius: []review.Tag{review.TagContracts}}},
		{Verdict: review.Verdict{BlastRadius: []review.Tag{review.TagMigrations}}},
	}

	drivers, ok := reviewverdict.TopRiskDrivers(records)
	if !ok {
		t.Fatalf("TopRiskDrivers(records) ok = false, want true")
	}
	if len(drivers) != 3 {
		t.Fatalf("TopRiskDrivers(records) = %d tags, want 3", len(drivers))
	}

	// auth: 2, migrations: 2 (tie, alphabetical: auth < migrations), contracts: 1.
	want := []reviewverdict.TagCount{
		{Tag: review.TagAuth, Count: 2},
		{Tag: review.TagMigrations, Count: 2},
		{Tag: review.TagContracts, Count: 1},
	}
	for i, w := range want {
		if drivers[i] != w {
			t.Errorf("drivers[%d] = %+v, want %+v", i, drivers[i], w)
		}
	}
}
