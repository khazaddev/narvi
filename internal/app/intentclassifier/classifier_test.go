package intentclassifier

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/app/ports"
	intentdomain "github.com/narvidev/narvi/internal/domain/intent"
)

// --- test fakes ---

type fakeLLM struct {
	response json.RawMessage
	err      error
	calls    int
	// costUSD is the CompletionResponse.CostUSD this fake reports on a
	// successful Complete call -- nil by default, exactly matching a real
	// ports.LLM implementation that either never computes cost, or (the
	// Anthropic adapter's own case) genuinely couldn't for this model.
	costUSD *float64
}

func (f *fakeLLM) Complete(_ context.Context, _ ports.CompletionRequest) (ports.CompletionResponse, error) {
	f.calls++
	if f.err != nil {
		return ports.CompletionResponse{}, f.err
	}
	return ports.CompletionResponse{Raw: f.response, CostUSD: f.costUSD}, nil
}

type fakeTemplates struct {
	templates map[string]string
	err       error
}

func (f *fakeTemplates) GetTemplate(_ context.Context, name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	tmpl, ok := f.templates[name]
	if !ok {
		return "", errors.New("intentclassifier test: template not found")
	}
	return tmpl, nil
}

func validTemplates() *fakeTemplates {
	return &fakeTemplates{templates: map[string]string{
		templateNameSystem: "Surface: {{surface}}.",
	}}
}

func brokenTemplates() *fakeTemplates {
	return &fakeTemplates{templates: map[string]string{
		templateNameSystem: "{{this_placeholder_does_not_exist}}",
	}}
}

func successResponse(target, mode, confidence, reasoning string) json.RawMessage {
	raw, err := json.Marshal(structuredOutput{Target: target, Mode: mode, Confidence: confidence, Reasoning: reasoning})
	if err != nil {
		panic(err)
	}
	return raw
}

// --- Classify: happy path ---

func TestService_Classify_Success(t *testing.T) {
	llm := &fakeLLM{response: successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "clear ask to review")}
	svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)

	decision := svc.Classify(context.Background(), ports.IntentClassifierInput{
		Text:    "please review this PR",
		Surface: "github",
	})

	if decision.Source != ports.IntentSourceClassifier {
		t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceClassifier)
	}
	if decision.Target != intentdomain.TargetReview {
		t.Errorf("Target = %q, want %q", decision.Target, intentdomain.TargetReview)
	}
	if decision.Mode != intentdomain.ModeBuild {
		t.Errorf("Mode = %q, want %q", decision.Mode, intentdomain.ModeBuild)
	}
	if decision.Confidence != intentdomain.ConfidenceHigh {
		t.Errorf("Confidence = %q, want %q", decision.Confidence, intentdomain.ConfidenceHigh)
	}
	if decision.Reasoning != "clear ask to review" {
		t.Errorf("Reasoning = %q, want %q", decision.Reasoning, "clear ask to review")
	}
	if decision.FallbackReason != "" {
		t.Errorf("FallbackReason = %q, want empty on classifier success", decision.FallbackReason)
	}
	if llm.calls != 1 {
		t.Errorf("llm.calls = %d, want 1", llm.calls)
	}
}

// --- Classify: never-throw contract across every failure mode ---

func TestService_Classify_NeverThrows(t *testing.T) {
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
			templates:  brokenTemplates(),
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "llm no_api_key",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeNoAPIKey, Provider: "anthropic"}},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonNoAPIKey,
		},
		{
			name:       "llm timeout",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeTimeout, Provider: "anthropic"}},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonTimeout,
		},
		{
			name:       "llm invalid_output",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeInvalidOutput, Provider: "anthropic"}},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
		{
			name:       "llm api_error",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeAPIError, Provider: "anthropic"}},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "llm unsupported_provider",
			llm:        &fakeLLM{err: &ports.LLMError{Code: ports.CodeUnsupportedProvider, Provider: "anthropic"}},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonUnsupportedProvider,
		},
		{
			name:       "llm returns a plain, non-typed error (a bug in some other ports.LLM implementation)",
			llm:        &fakeLLM{err: errors.New("some untyped failure")},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonAPIError,
		},
		{
			name:       "llm returns malformed JSON",
			llm:        &fakeLLM{response: json.RawMessage(`not valid json`)},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
		{
			name:       "llm returns a schema-shaped but out-of-enum value",
			llm:        &fakeLLM{response: successResponse("banana", intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "x")},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
		{
			name:       "llm returns empty structured output",
			llm:        &fakeLLM{response: json.RawMessage(`{}`)},
			templates:  validTemplates(),
			wantReason: ports.FallbackReasonInvalidOutput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(tt.llm, "anthropic", "claude-haiku-4-5", tt.templates, nil, nil)

			decision := svc.Classify(context.Background(), ports.IntentClassifierInput{Text: "hello", Surface: "web"})

			if decision.Source != ports.IntentSourceFallback {
				t.Fatalf("Source = %q, want %q (never a caller-fatal error, always resolves to a decision)", decision.Source, ports.IntentSourceFallback)
			}
			if decision.FallbackReason != tt.wantReason {
				t.Errorf("FallbackReason = %q, want %q", decision.FallbackReason, tt.wantReason)
			}
			if decision.Confidence != "" || decision.Reasoning != "" || decision.Target != "" || decision.Mode != "" {
				t.Errorf("fallback decision carries non-empty classifier-only fields: %+v", decision)
			}
		})
	}
}

// TestService_Classify_FallbackHonorsDeterministicTarget is §8.2's own
// audit fix (§5.2/§18.2): a fallback decision must still carry
// input.DeterministicTarget when the caller supplied one, across EVERY
// fallback branch (template fetch, template assemble, LLM error, invalid
// output) -- not only the happy-path corroboration step, which never runs
// at all once the model-based path has already failed. Before this fix,
// fallbackDecision unconditionally discarded DeterministicTarget, so a
// caller like GitHub's own ingress (which always supplies
// intentdomain.TargetReview, a purely structural signal never derived
// from the model) would have silently lost that signal the moment the
// classifier's own LLM call degraded -- exactly the moment §5.2 says this
// matters most.
func TestService_Classify_FallbackHonorsDeterministicTarget(t *testing.T) {
	tests := []struct {
		name      string
		llm       *fakeLLM
		templates *fakeTemplates
	}{
		{
			name:      "template fetch error",
			llm:       &fakeLLM{},
			templates: &fakeTemplates{err: errors.New("db unreachable")},
		},
		{
			name:      "template assemble error",
			llm:       &fakeLLM{},
			templates: brokenTemplates(),
		},
		{
			name:      "llm error",
			llm:       &fakeLLM{err: &ports.LLMError{Code: ports.CodeTimeout, Provider: "anthropic"}},
			templates: validTemplates(),
		},
		{
			name:      "llm returns a non-typed error",
			llm:       &fakeLLM{err: errors.New("some untyped failure")},
			templates: validTemplates(),
		},
		{
			name:      "llm returns invalid output",
			llm:       &fakeLLM{response: json.RawMessage(`not valid json`)},
			templates: validTemplates(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(tt.llm, "anthropic", "claude-haiku-4-5", tt.templates, nil, nil)

			decision := svc.Classify(context.Background(), ports.IntentClassifierInput{
				Text:                "hello",
				Surface:             "github",
				DeterministicTarget: intentdomain.TargetReview,
			})

			if decision.Source != ports.IntentSourceFallback {
				t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceFallback)
			}
			if decision.Target != intentdomain.TargetReview {
				t.Errorf("Target = %q, want %q (the deterministic signal must survive a fallback)", decision.Target, intentdomain.TargetReview)
			}
		})
	}
}

// TestService_Classify_FallbackNoDeterministicTarget proves the converse:
// a caller that supplies NO deterministic signal at all (Slack/Linear's
// own current callers) still gets an empty Target on fallback, exactly
// like before this fix -- the fix only ever surfaces a signal that was
// genuinely supplied, never fabricates one.
func TestService_Classify_FallbackNoDeterministicTarget(t *testing.T) {
	svc := New(&fakeLLM{err: &ports.LLMError{Code: ports.CodeTimeout, Provider: "anthropic"}}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)

	decision := svc.Classify(context.Background(), ports.IntentClassifierInput{Text: "hello", Surface: "slack"})

	if decision.Target != "" {
		t.Errorf("Target = %q, want empty (no DeterministicTarget was supplied)", decision.Target)
	}
}

// --- Classify: deterministic Target corroboration ---

func TestService_Classify_Corroboration(t *testing.T) {
	tests := []struct {
		name                string
		classifierTarget    string
		deterministicTarget string
		wantTarget          string
		wantConfidence      string
	}{
		{
			name:                "no deterministic signal: raw classifier output passes through unchanged",
			classifierTarget:    intentdomain.TargetReview,
			deterministicTarget: "",
			wantTarget:          intentdomain.TargetReview,
			wantConfidence:      intentdomain.ConfidenceHigh,
		},
		{
			name:                "signals agree: confidence unchanged",
			classifierTarget:    intentdomain.TargetReview,
			deterministicTarget: intentdomain.TargetReview,
			wantTarget:          intentdomain.TargetReview,
			wantConfidence:      intentdomain.ConfidenceHigh,
		},
		{
			name:                "signals disagree: deterministic wins the recorded target, confidence forced low",
			classifierTarget:    intentdomain.TargetRequest,
			deterministicTarget: intentdomain.TargetReview,
			wantTarget:          intentdomain.TargetReview,
			wantConfidence:      intentdomain.ConfidenceLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &fakeLLM{response: successResponse(tt.classifierTarget, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "reasoning text")}
			svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)

			decision := svc.Classify(context.Background(), ports.IntentClassifierInput{
				Text:                "some input",
				Surface:             "github",
				DeterministicTarget: tt.deterministicTarget,
			})

			if decision.Source != ports.IntentSourceClassifier {
				t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceClassifier)
			}
			if decision.Target != tt.wantTarget {
				t.Errorf("Target = %q, want %q", decision.Target, tt.wantTarget)
			}
			if decision.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %q, want %q", decision.Confidence, tt.wantConfidence)
			}
		})
	}
}

// --- IsActive: shadow-mode gating never affects Classify's own output ---

func TestService_IsActive(t *testing.T) {
	svc := New(&fakeLLM{}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, []string{"github", "slack"})

	tests := []struct {
		surface string
		want    bool
	}{
		{"github", true},
		{"slack", true},
		{"linear", false},
		{"web", false},
		{"", false},
		{"not-a-real-surface", false},
	}
	for _, tt := range tests {
		if got := svc.IsActive(tt.surface); got != tt.want {
			t.Errorf("IsActive(%q) = %v, want %v", tt.surface, got, tt.want)
		}
	}
}

func TestService_IsActive_DefaultsShadowWhenUnconfigured(t *testing.T) {
	svc := New(&fakeLLM{}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)
	for _, surface := range []string{"web", "slack", "linear", "github"} {
		if svc.IsActive(surface) {
			t.Errorf("IsActive(%q) = true with no configured active surfaces, want false (§18.5: every surface defaults to shadow)", surface)
		}
	}
}

// TestService_Classify_ShadowModeNeverChangesDecision proves that
// IsActive (which callers consult to decide whether to ACT on Target/
// Mode) has zero influence on what Classify itself computes -- the SAME
// LLM response produces the SAME IntentDecision regardless of whether the
// surface is configured active or shadow. Shadow mode is purely a
// caller-side gate on ACTING on the decision, never on the decision's own
// content or on whether it gets recorded.
func TestService_Classify_ShadowModeNeverChangesDecision(t *testing.T) {
	response := successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "reasoning")

	shadowSvc := New(&fakeLLM{response: response}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)
	activeSvc := New(&fakeLLM{response: response}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, []string{"github"})

	input := ports.IntentClassifierInput{Text: "please review", Surface: "github"}

	shadowDecision := shadowSvc.Classify(context.Background(), input)
	activeDecision := activeSvc.Classify(context.Background(), input)

	if shadowSvc.IsActive("github") {
		t.Fatal("shadowSvc.IsActive(\"github\") = true, want false")
	}
	if !activeSvc.IsActive("github") {
		t.Fatal("activeSvc.IsActive(\"github\") = false, want true")
	}
	if shadowDecision != activeDecision {
		t.Errorf("decision differs between shadow (%+v) and active (%+v) configuration -- Classify must be identical regardless of IsActive", shadowDecision, activeDecision)
	}
}

// --- RecordDecision: write-once persistence ---

type fakeSessionStore struct {
	set map[pgtype.UUID]bool
	// payloads captures the decisionJSON bytes passed to the most recent
	// UpdateIntentDecisionIfNull call for a given id -- lets tests
	// unmarshal and inspect the actual persisted intentdomain.
	// IntentDecisionRecord (e.g. its CostUSD field) rather than only the
	// win/lose outcome the set map already tracks.
	payloads map[pgtype.UUID][]byte
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{set: make(map[pgtype.UUID]bool), payloads: make(map[pgtype.UUID][]byte)}
}

func (f *fakeSessionStore) UpdateIntentDecisionIfNull(_ context.Context, id pgtype.UUID, decisionJSON []byte) (bool, error) {
	f.payloads[id] = decisionJSON
	if f.set[id] {
		return false, nil
	}
	f.set[id] = true
	return true, nil
}

func testSessionID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}
}

func TestService_RecordDecision_FirstWins(t *testing.T) {
	sessions := newFakeSessionStore()
	svc := New(&fakeLLM{}, "anthropic", "claude-haiku-4-5", validTemplates(), sessions, nil)

	rec := intentdomain.IntentDecisionRecord{
		Surface:        "github",
		Source:         intentdomain.RecordSourceClassifier,
		Target:         intentdomain.TargetReview,
		Mode:           intentdomain.ModeBuild,
		DecidedAtStage: intentdomain.DecidedAtStageCreate,
	}

	id := testSessionID()

	won1, err := svc.RecordDecision(context.Background(), id, rec)
	if err != nil {
		t.Fatalf("first RecordDecision error = %v, want nil", err)
	}
	if !won1 {
		t.Error("first RecordDecision won = false, want true")
	}

	won2, err := svc.RecordDecision(context.Background(), id, rec)
	if err != nil {
		t.Fatalf("second RecordDecision error = %v, want nil", err)
	}
	if won2 {
		t.Error("second RecordDecision won = true, want false (first decision wins)")
	}
}
