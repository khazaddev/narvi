package intent

import (
	"strings"
	"testing"
)

func TestTruncateReasoning(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"short string well under bound", "the user asked for a code review"},
		{"exactly at bound", strings.Repeat("a", MaxReasoningLength)},
		{"one over bound", strings.Repeat("a", MaxReasoningLength+1)},
		{"far over bound", strings.Repeat("a", MaxReasoningLength*10)},
		{"multi-byte runes far over bound", strings.Repeat("é", MaxReasoningLength+5)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateReasoning(tt.input)
			gotRunes := []rune(got)
			if len(gotRunes) > MaxReasoningLength {
				t.Fatalf("TruncateReasoning returned %d runes, want <= %d", len(gotRunes), MaxReasoningLength)
			}

			inputRunes := []rune(tt.input)
			if len(inputRunes) <= MaxReasoningLength {
				if got != tt.input {
					t.Errorf("input within bound was mutated: got %q, want %q", got, tt.input)
				}
				return
			}

			want := string(inputRunes[:MaxReasoningLength])
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}

			if !strings.HasPrefix(got, string(inputRunes[:1])) {
				t.Errorf("truncated output does not start with the original prefix")
			}
		})
	}
}

func TestTruncateReasoning_NeverRejectsOnlyCuts(t *testing.T) {
	// §18.4: "never rejected outright for being long, just cut off" --
	// TruncateReasoning has no error return at all, so this is really a
	// compile-time guarantee, but assert the runtime behavior explicitly
	// too: a pathologically long input still comes back as a valid,
	// non-empty, bounded string rather than panicking or returning "".
	huge := strings.Repeat("x", 1_000_000)
	got := TruncateReasoning(huge)
	if got == "" {
		t.Fatal("TruncateReasoning returned empty string for a huge input")
	}
	if len([]rune(got)) != MaxReasoningLength {
		t.Fatalf("got %d runes, want exactly %d", len([]rune(got)), MaxReasoningLength)
	}
}
