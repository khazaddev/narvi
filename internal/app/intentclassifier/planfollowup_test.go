package intentclassifier

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/app/ports"
	intentdomain "github.com/narvidev/narvi/internal/domain/intent"
)

// validPlanFollowupTemplates/brokenPlanFollowupTemplates mirror
// validTemplates/brokenTemplates (classifier_test.go), one category over --
// keyed by templateNamePlanFollowup instead of templateNameSystem, and with
// no {{surface}} placeholder at all (ClassifyPlanFollowup's own doc
// comment: this category's template references none).
func validPlanFollowupTemplates() *fakeTemplates {
	return &fakeTemplates{templates: map[string]string{
		templateNamePlanFollowup: "Classify this reply to an awaiting-approval plan.",
	}}
}

func brokenPlanFollowupTemplates() *fakeTemplates {
	return &fakeTemplates{templates: map[string]string{
		templateNamePlanFollowup: "{{this_placeholder_does_not_exist}}",
	}}
}

func successPlanFollowupResponse(target, confidence, reasoning string) json.RawMessage {
	raw, err := json.Marshal(planFollowupStructuredOutput{Target: target, Confidence: confidence, Reasoning: reasoning})
	if err != nil {
		panic(err)
	}
	return raw
}

// --- ClassifyPlanFollowup: happy path ---

func TestService_ClassifyPlanFollowup_Success_Amend(t *testing.T) {
	costUSD := 0.0021
	llm := &fakeLLM{
		response: successPlanFollowupResponse(intentdomain.TargetAmend, intentdomain.ConfidenceHigh, "explicitly asks for a different approach"),
		costUSD:  &costUSD,
	}
	svc := New(llm, "anthropic", "claude-haiku-4-5", validPlanFollowupTemplates(), nil, nil)

	decision := svc.ClassifyPlanFollowup(context.Background(), "actually let's use approach B instead")

	if decision.Source != ports.IntentSourceClassifier {
		t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceClassifier)
	}
	if decision.Target != intentdomain.TargetAmend {
		t.Errorf("Target = %q, want %q", decision.Target, intentdomain.TargetAmend)
	}
	if decision.Confidence != intentdomain.ConfidenceHigh {
		t.Errorf("Confidence = %q, want %q", decision.Confidence, intentdomain.ConfidenceHigh)
	}
	if decision.Reasoning != "explicitly asks for a different approach" {
		t.Errorf("Reasoning = %q, want %q", decision.Reasoning, "explicitly asks for a different approach")
	}
	// Mode is NEVER populated for this category -- there is no second
	// decision axis (schema_planfollowup.go's own doc comment).
	if decision.Mode != "" {
		t.Errorf("Mode = %q, want empty (plan_followup has no second decision axis)", decision.Mode)
	}
	if decision.FallbackReason != "" {
		t.Errorf("FallbackReason = %q, want empty on classifier success", decision.FallbackReason)
	}
	if decision.CostUSD == nil || *decision.CostUSD != costUSD {
		t.Errorf("CostUSD = %v, want %v", decision.CostUSD, costUSD)
	}
	if llm.calls != 1 {
		t.Errorf("llm.calls = %d, want 1", llm.calls)
	}
}

func TestService_ClassifyPlanFollowup_Success_Answer(t *testing.T) {
	llm := &fakeLLM{
		response: successPlanFollowupResponse(intentdomain.TargetAnswer, intentdomain.ConfidenceHigh, "just answering a clarifying question, no change requested"),
	}
	svc := New(llm, "anthropic", "claude-haiku-4-5", validPlanFollowupTemplates(), nil, nil)

	decision := svc.ClassifyPlanFollowup(context.Background(), "yes, the staging DB")

	if decision.Source != ports.IntentSourceClassifier {
		t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceClassifier)
	}
	if decision.Target != intentdomain.TargetAnswer {
		t.Errorf("Target = %q, want %q", decision.Target, intentdomain.TargetAnswer)
	}
}

// --- ClassifyPlanFollowup: never-throw contract across every failure mode ---

func TestService_ClassifyPlanFollowup_NeverThrows(t *testing.T) {
	tests := []struct {
		name       string
		llm        *fakeLLM
		templates  *fakeTemplates
		wantReason string
	}{
		{
			name:       "template fetch error",
			llm:        &fakeLLM{},
			templates:  &fakeTemplates{err: errors.New("db unreachable")},
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "template assemble error (unknown placeholder)",
			llm:        &fakeLLM{},
			templates:  brokenPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "llm no_api_key",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeNoAPIKey, Provider: "anthropic"}},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonNoAPIKey,
		},
		{
			name:       "llm timeout",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeTimeout, Provider: "anthropic"}},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonTimeout,
		},
		{
			name:       "llm api_error",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeAPIError, Provider: "anthropic"}},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "llm unsupported_provider",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeUnsupportedProvider, Provider: "anthropic"}},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonUnsupportedProvider,
		},
		{
			name:       "llm returns a plain, non-typed error",
			llm:        &fakeLLM{err: errors.New("some untyped failure")},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "llm returns malformed JSON",
			llm:        &fakeLLM{response: json.RawMessage(`not valid json`)},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
		{
			name:       "llm returns a schema-shaped but out-of-enum target",
			llm:        &fakeLLM{response: successPlanFollowupResponse("banana", intentdomain.ConfidenceHigh, "x")},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
		{
			// F7/H1 (adversarial review): planFollowupStructuredOutput.valid()
			// (schema_planfollowup.go) checks target THEN confidence, via two
			// SEQUENTIAL switch statements -- every other case in this table
			// pairs a valid target with a valid confidence, or an invalid
			// target (caught by the FIRST switch before confidence is ever
			// reached), so the confidence-enum check itself was never actually
			// exercised by this suite (confirmed by mutation: deleting that
			// second switch left every pre-existing test here green). This
			// case pairs a VALID target with an out-of-enum confidence value
			// ("very high", not one of high/medium/low) specifically to
			// exercise that second switch -- promoted from a stray, untracked
			// probe file (zztmp_probe_test.go) left behind by the review pass,
			// now deleted.
			name:       "llm returns a schema-shaped but out-of-enum confidence",
			llm:        &fakeLLM{response: successPlanFollowupResponse(intentdomain.TargetAmend, "very high", "x")},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
		{
			name:       "llm returns empty structured output",
			llm:        &fakeLLM{response: json.RawMessage(`{}`)},
			templates:  validPlanFollowupTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(tt.llm, "anthropic", "claude-haiku-4-5", tt.templates, nil, nil)

			decision := svc.ClassifyPlanFollowup(context.Background(), "some reply")

			if decision.Source != ports.IntentSourceFallback {
				t.Fatalf("Source = %q, want %q (never a caller-fatal error, always resolves to a decision)", decision.Source, ports.IntentSourceFallback)
			}
			if decision.FallbackReason != tt.wantReason {
				t.Errorf("FallbackReason = %q, want %q", decision.FallbackReason, tt.wantReason)
			}
			if decision.Confidence != "" || decision.Reasoning != "" || decision.Target != "" {
				t.Errorf("fallback decision carries non-empty classifier-only fields: %+v", decision)
			}
		})
	}
}

// TestService_ClassifyPlanFollowup_UsesOwnTemplateName proves this method
// reads from templateNamePlanFollowup, NOT templateNameSystem -- a fake
// templates store that only knows the OTHER category's name must still
// fail open here (mirrors the "single shared Service, per-category
// template" design §23.1 requires -- this method must never silently fall
// back to the review-vs-request template).
func TestService_ClassifyPlanFollowup_UsesOwnTemplateName(t *testing.T) {
	llm := &fakeLLM{response: successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "wrong category entirely")}
	svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)

	decision := svc.ClassifyPlanFollowup(context.Background(), "some reply")

	if decision.Source != ports.IntentSourceFallback {
		t.Fatalf("Source = %q, want %q -- a templates store with no plan_followup entry must fail open, not silently reuse the system template", decision.Source, ports.IntentSourceFallback)
	}
	if decision.FallbackReason != ports.FallbackReasonAPIError {
		t.Errorf("FallbackReason = %q, want %q", decision.FallbackReason, ports.FallbackReasonAPIError)
	}
	if llm.calls != 0 {
		t.Errorf("llm.calls = %d, want 0 (should fail before ever reaching the LLM)", llm.calls)
	}
}
