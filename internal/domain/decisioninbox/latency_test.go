package decisioninbox_test

import (
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
)

func TestMedianLatency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []time.Duration
		want time.Duration
		ok   bool
	}{
		{"empty is not yet computed", nil, 0, false},
		{"single value", []time.Duration{5 * time.Hour}, 5 * time.Hour, true},
		{"odd count picks the middle value", []time.Duration{1 * time.Hour, 5 * time.Hour, 3 * time.Hour}, 3 * time.Hour, true},
		{"even count averages the two middle values", []time.Duration{1 * time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour}, 150 * time.Minute, true},
		{"input order does not matter", []time.Duration{9 * time.Hour, 1 * time.Hour, 5 * time.Hour}, 5 * time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := decisioninbox.MedianLatency(tc.in)
			if ok != tc.ok {
				t.Fatalf("MedianLatency(%v) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("MedianLatency(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMedianLatency_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	in := []time.Duration{9 * time.Hour, 1 * time.Hour, 5 * time.Hour}
	original := append([]time.Duration(nil), in...)

	if _, ok := decisioninbox.MedianLatency(in); !ok {
		t.Fatal("MedianLatency() ok = false, want true")
	}
	for i := range in {
		if in[i] != original[i] {
			t.Fatalf("MedianLatency() mutated its input: got %v, want %v", in, original)
		}
	}
}
