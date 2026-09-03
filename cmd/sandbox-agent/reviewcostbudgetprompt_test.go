// This file (deliberately NOT behind the "integration" build tag, mirrors
// reviewverdicttoolprompt_test.go's own identical precedent -- fast enough
// for the default `go test ./...`/`go test -race` suite) proves
// renderReviewCostBudgetToolPromptText (reviewcostbudgetprompt.go)
// directly, in isolation.
package main

import (
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
)

func TestRenderReviewCostBudgetToolPromptText(t *testing.T) {
	t.Parallel()

	reviewPromptText := "please review this PR.\n\n" +
		"GET " + review.ReviewCostBudgetToolURLPlaceholder + "?ceilingUsd=5.00\n"

	tests := []struct {
		name         string
		text         string
		budgetURL    string
		wantExact    string
		wantContains []string
	}{
		{
			name:      "no placeholder present: byte-for-byte no-op regardless of budgetURL",
			text:      "an ordinary build turn's own prompt, nothing review-shaped here",
			budgetURL: "http://127.0.0.1:12345/review-cost-budget",
			wantExact: "an ordinary build turn's own prompt, nothing review-shaped here",
		},
		{
			name:      "empty budgetURL (no live session ever started the server): no-op even with the placeholder present",
			text:      reviewPromptText,
			budgetURL: "",
			wantExact: reviewPromptText,
		},
		{
			name:      "review turn, real budgetURL: placeholder resolved",
			text:      reviewPromptText,
			budgetURL: "http://127.0.0.1:54321/review-cost-budget",
			wantContains: []string{
				"GET http://127.0.0.1:54321/review-cost-budget?ceilingUsd=5.00",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderReviewCostBudgetToolPromptText(tt.text, tt.budgetURL)

			if tt.wantExact != "" {
				if got != tt.wantExact {
					t.Errorf("renderReviewCostBudgetToolPromptText() = %q, want exactly %q", got, tt.wantExact)
				}
				return
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("renderReviewCostBudgetToolPromptText() = %q, want it to contain %q", got, want)
				}
			}
			if strings.Contains(got, review.ReviewCostBudgetToolURLPlaceholder) {
				t.Errorf("renderReviewCostBudgetToolPromptText() = %q, still contains the unresolved placeholder", got)
			}
		})
	}
}

// TestRenderReviewCostBudgetToolPromptText_PreservesSurroundingText proves
// the substitution touches only the placeholder token itself, never
// mangling the caller's own surrounding prompt text (mirrors
// TestRenderVerdictToolPromptText's own "byte-for-byte no-op" precedent,
// extended to the resolved case).
func TestRenderReviewCostBudgetToolPromptText_PreservesSurroundingText(t *testing.T) {
	t.Parallel()

	text := "Before spawning fact-check, first GET " + review.ReviewCostBudgetToolURLPlaceholder + "?ceilingUsd=0.50\nThen decide."
	got := renderReviewCostBudgetToolPromptText(text, "http://127.0.0.1:9999/review-cost-budget")

	want := "Before spawning fact-check, first GET http://127.0.0.1:9999/review-cost-budget?ceilingUsd=0.50\nThen decide."
	if got != want {
		t.Errorf("renderReviewCostBudgetToolPromptText() =\n%q\nwant:\n%q", got, want)
	}
}
