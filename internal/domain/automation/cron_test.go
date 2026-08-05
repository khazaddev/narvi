package automation_test

import (
	"errors"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/automation"
)

func TestValidateCronExpr(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr error
	}{
		{"every minute", "* * * * *", nil},
		{"specific time", "0 2 * * *", nil},
		{"step", "*/15 * * * *", nil},
		{"list", "0,30 9,17 * * *", nil},
		{"range", "0 9-17 * * *", nil},
		{"range with step", "0 9-17/2 * * *", nil},
		{"weekdays", "0 9 * * 1-5", nil},
		{"too few fields", "* * * *", automation.ErrCronFieldCount},
		{"too many fields", "* * * * * *", automation.ErrCronFieldCount},
		{"empty", "", automation.ErrCronFieldCount},
		{"non-numeric field", "a * * * *", automation.ErrCronFieldSyntax},
		{"minute out of range", "60 * * * *", automation.ErrCronFieldRange},
		{"hour out of range", "0 24 * * *", automation.ErrCronFieldRange},
		{"day-of-month zero", "0 0 0 * *", automation.ErrCronFieldRange},
		{"month out of range", "0 0 1 13 *", automation.ErrCronFieldRange},
		{"day-of-week out of range", "0 0 * * 7", automation.ErrCronFieldRange},
		{"backwards range", "0 17-9 * * *", automation.ErrCronFieldSyntax},
		{"empty comma item", "0,,0 * * * *", automation.ErrCronFieldSyntax},
		{"bad step", "*/0 * * * *", automation.ErrCronFieldSyntax},
		{"bad step non-numeric", "*/x * * * *", automation.ErrCronFieldSyntax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := automation.ValidateCronExpr(tt.expr)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCronMatches(t *testing.T) {
	// 2026-08-03 is a Monday.
	mon0200 := time.Date(2026, time.August, 3, 2, 0, 0, 0, time.UTC)
	mon0201 := time.Date(2026, time.August, 3, 2, 1, 0, 0, time.UTC)
	sun0200 := time.Date(2026, time.August, 2, 2, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		expr string
		t    time.Time
		want bool
	}{
		{"every minute matches anything", "* * * * *", mon0201, true},
		{"exact minute match", "0 2 * * *", mon0200, true},
		{"exact minute no match", "0 2 * * *", mon0201, false},
		{"weekday filter matches monday", "0 2 * * 1-5", mon0200, true},
		{"weekday filter excludes sunday", "0 2 * * 1-5", sun0200, false},
		{"step matches every 15", "*/15 * * * *", time.Date(2026, time.August, 3, 2, 30, 0, 0, time.UTC), true},
		{"step excludes non-multiple", "*/15 * * * *", time.Date(2026, time.August, 3, 2, 31, 0, 0, time.UTC), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := automation.CronMatches(tt.expr, tt.t)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("CronMatches(%q, %v) = %v, want %v", tt.expr, tt.t, got, tt.want)
			}
		})
	}
}

func TestCronMatchesInvalidExpr(t *testing.T) {
	_, err := automation.CronMatches("not a cron expr", time.Now())
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestCronMatchesConvertsToUTC(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*60*60)
	// 21:00 in UTC-5 is 02:00 the next day in UTC.
	localTime := time.Date(2026, time.August, 2, 21, 0, 0, 0, loc)

	got, err := automation.CronMatches("0 2 * * *", localTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected CronMatches to convert to UTC before comparing, got false")
	}
}
