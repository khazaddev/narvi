package intentclassifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/app/ports"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
)

// TemplateFetcher is the minimal template-lookup dependency Classify
// needs -- see doc.go for why this is a narrow, local interface rather
// than *postgres.PromptTemplateStore directly.
type TemplateFetcher interface {
	GetTemplate(ctx context.Context, name string) (string, error)
}

// DecisionStore is the minimal write-once-persistence dependency
// RecordDecision needs -- structurally identical to
// *postgres.SessionStore.UpdateIntentDecisionIfNull's own signature (see
// doc.go).
type DecisionStore interface {
	UpdateIntentDecisionIfNull(ctx context.Context, id pgtype.UUID, decisionJSON []byte) (won bool, err error)
}

// Service is the real ports.IntentClassifier implementation.
type Service struct {
	llm       ports.LLM
	provider  string
	model     string
	templates TemplateFetcher
	sessions  DecisionStore
	// activeSurfaces is §18.5's permanent shadow-vs-active gate: a
	// surface present (true) here drives real Mode/Target behavior; any
	// other surface (absent, or explicitly false) is shadow -- computed
	// and recorded only, never consumed for real behavior. Built once, at
	// construction, from platform.Config.IntentClassifierActiveSurfaces.
	activeSurfaces map[string]bool
}

var _ ports.IntentClassifier = (*Service)(nil)

// New builds a Service. provider/model are the configured
// ports.CompletionRequest.Provider/Model this Service passes on every
// Complete call (platform.Config.IntentClassifierProvider/Model).
// activeSurfaces lists which ingress surfaces run "active" (§18.5) --
// every surface NOT listed defaults to shadow mode.
func New(llmClient ports.LLM, provider, model string, templates TemplateFetcher, sessions DecisionStore, activeSurfaces []string) *Service {
	active := make(map[string]bool, len(activeSurfaces))
	for _, surface := range activeSurfaces {
		active[surface] = true
	}
	return &Service{
		llm:            llmClient,
		provider:       provider,
		model:          model,
		templates:      templates,
		sessions:       sessions,
		activeSurfaces: active,
	}
}

// IsActive implements §18.5's permanent shadow-vs-active gate for
// surface. A surface never explicitly configured active defaults to
// shadow -- §18.5: "never silently flip a surface to active without an
// explicit config value saying so".
func (s *Service) IsActive(surface string) bool {
	return s.activeSurfaces[surface]
}

// Classify implements ports.IntentClassifier (§18.1's never-throw
// contract): every code path below resolves to a returned
// ports.IntentDecision, never a caller-fatal error.
func (s *Service) Classify(ctx context.Context, input ports.IntentClassifierInput) ports.IntentDecision {
	rawTemplate, err := s.templates.GetTemplate(ctx, templateNameSystem)
	if err != nil {
		return fallbackDecision(ports.FallbackReasonAPIError)
	}

	systemPrompt, err := intentdomain.AssembleTemplate(rawTemplate, map[string]string{
		"surface": input.Surface,
	})
	if err != nil {
		// A template that fails to assemble (an unknown placeholder) is
		// an admin/operator authoring bug in prompt_templates, not a
		// per-request failure this classifier can recover from mid-call
		// -- §18.1's never-throw contract still applies: fall back
		// rather than propagate.
		return fallbackDecision(ports.FallbackReasonAPIError)
	}

	raw, err := s.llm.Complete(ctx, ports.CompletionRequest{
		Provider:       s.provider,
		Model:          s.model,
		System:         systemPrompt,
		Messages:       []ports.CompletionMessage{{Role: "user", Content: input.Text}},
		ResponseSchema: responseSchema,
	})
	if err != nil {
		return fallbackFromLLMError(err)
	}

	var parsed structuredOutput
	if unmarshalErr := json.Unmarshal(raw, &parsed); unmarshalErr != nil || !parsed.valid() {
		return fallbackDecision(ports.FallbackReasonInvalidOutput)
	}

	target := parsed.Target
	confidence := parsed.Confidence

	// Deterministic Target corroboration (§18.2): only meaningful when the
	// calling surface actually supplied an independent signal to
	// corroborate against -- see internal/domain/intent.CorroborateTarget's
	// own doc comment for the full "irreversible action" reasoning this
	// mirrors. On disagreement (or, per this Service's own judgment call,
	// on there being no deterministic signal to corroborate against at
	// all for a caller that DID supply DeterministicTarget as non-empty --
	// which cannot actually happen here, since CorroborateTarget only
	// treats "" as "no signal"), confidence is forced to "low" rather
	// than silently trusting the raw classifier Target: this surfaces the
	// disagreement through the SAME confidence field a future consumer
	// already inspects via intentdomain.DeriveNeedsClarification, without
	// requiring a new field on the fixed ports.IntentDecision contract.
	if input.DeterministicTarget != "" {
		finalTarget, corroborated := intentdomain.CorroborateTarget(target, input.DeterministicTarget, true)
		target = finalTarget
		if !corroborated {
			confidence = intentdomain.ConfidenceLow
		}
	}

	return ports.IntentDecision{
		Source:     ports.IntentSourceClassifier,
		Target:     target,
		Mode:       parsed.Mode,
		Confidence: confidence,
		Reasoning:  intentdomain.TruncateReasoning(parsed.Reasoning),
	}
}

// fallbackDecision builds the fixed fallback shape (§18.1) for reason.
func fallbackDecision(reason string) ports.IntentDecision {
	return ports.IntentDecision{
		Source:         ports.IntentSourceFallback,
		FallbackReason: reason,
	}
}

// fallbackFromLLMError maps a ports.LLM.Complete failure onto
// IntentDecision.FallbackReason -- via the *ports.LLMError.Code TYPED
// error, never by string-matching err's own message (§18.1).
// LLMErrorCode's five values and FallbackReason's five values are the
// SAME strings (ports/llm_test.go's own TestLLMErrorCode_MatchesFallbackReason
// locks this), so this is a direct 1:1 mapping.
func fallbackFromLLMError(err error) ports.IntentDecision {
	var llmErr *ports.LLMError
	if errors.As(err, &llmErr) {
		return fallbackDecision(string(llmErr.Code))
	}
	// A ports.LLM implementation that returns a non-*ports.LLMError is
	// itself a bug in that implementation (the port's own contract
	// requires typed errors) -- fall back to the generic api_error
	// reason rather than propagate, per §18.1's never-throw contract,
	// never panicking over another package's own bug.
	return fallbackDecision(ports.FallbackReasonAPIError)
}

// RecordDecision persists rec write-once onto sessionID's own
// intent_decision column (§18.4's guarded UPDATE) -- NOT part of the
// ports.IntentClassifier interface (Classify's own input carries no
// session id), so this lives only on the concrete *Service. won reports
// whether THIS call actually set the column (true) or a concurrent/
// earlier caller already had (false, "first decision wins") -- both are
// success outcomes for the caller; only a genuine marshal/database error
// is returned as err.
func (s *Service) RecordDecision(ctx context.Context, sessionID pgtype.UUID, rec intentdomain.IntentDecisionRecord) (won bool, err error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("intentclassifier: marshal decision record: %w", err)
	}
	return s.sessions.UpdateIntentDecisionIfNull(ctx, sessionID, payload)
}
