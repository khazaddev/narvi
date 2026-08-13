package reviewtriage_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/app/reviewtriage"
)

func TestResolveCounterReviewerModel(t *testing.T) {
	tests := []struct {
		name            string
		authoringModel  string
		wantEmpty       bool
		wantNotProvider string
	}{
		{"empty authoring model (human-authored PR, the common case) returns no override", "", true, ""},
		{"malformed authoring model (no slash) returns no override", "bogus-no-slash", true, ""},
		{"anthropic authoring model opposes to a non-anthropic provider", "anthropic/claude-opus-4-5", false, "anthropic"},
		{"openai authoring model opposes to a non-openai provider", "openai/gpt-5.4", false, "openai"},
		{"google authoring model opposes to a non-google provider", "google/gemini-2.5-flash", false, "google"},
		{"unrecognized provider in authoring model still opposes (no provider to skip)", "unknown-provider/some-model", false, ""},
		{"mixed-case authoring provider still excludes its own family (B10)", "Anthropic/claude-opus-4-5", false, "anthropic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewtriage.ResolveCounterReviewerModel(tt.authoringModel)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("ResolveCounterReviewerModel(%q) = %q, want empty (no override)", tt.authoringModel, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("ResolveCounterReviewerModel(%q) = \"\", want a real provider/model override", tt.authoringModel)
			}
			provider, model, ok := strings.Cut(got, "/")
			if !ok || provider == "" || model == "" {
				t.Fatalf("ResolveCounterReviewerModel(%q) = %q, want a \"provider/model\" shaped string", tt.authoringModel, got)
			}
			if tt.wantNotProvider != "" && provider == tt.wantNotProvider {
				t.Errorf("ResolveCounterReviewerModel(%q) = %q, provider must NEVER match the authoring family %q", tt.authoringModel, got, tt.wantNotProvider)
			}
		})
	}
}

// TestResolveCounterReviewerModel_Deterministic pins that repeated calls
// with the same input always return the identical result -- this
// function must never depend on map/slice iteration order (pickReasoningOrFirst's
// own doc comment).
func TestResolveCounterReviewerModel_Deterministic(t *testing.T) {
	first := reviewtriage.ResolveCounterReviewerModel("anthropic/claude-opus-4-5")
	for i := 0; i < 20; i++ {
		if got := reviewtriage.ResolveCounterReviewerModel("anthropic/claude-opus-4-5"); got != first {
			t.Fatalf("ResolveCounterReviewerModel is non-deterministic: call %d = %q, first call = %q", i, got, first)
		}
	}
}
