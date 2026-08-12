package intentclassifier

import (
	"context"
	"encoding/json"

	"github.com/khazaddev/narvi/internal/app/ports"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/platform"
)

// ClassifyPlanFollowup implements §23.1's plan_followup classification
// category (amend-vs-answer) -- the SAME never-throw contract (§18.1),
// confidence rubric (§18.2), and ports.IntentDecision shape Classify's own
// review-vs-request/plan-vs-build category already returns, just a
// different DB-backed template (templateNamePlanFollowup) and a different
// structured-output schema (planFollowupResponseSchema: target amend/
// answer, no mode field -- this category has only one decision axis). No
// parallel classification mechanism: this method reuses the SAME s.llm/
// s.templates collaborators, the SAME intentdomain.ConfidenceRubric/
// TruncateReasoning, and the SAME fallbackDecision/fallbackFromLLMError
// mapping functions Classify itself already established (classifier.go) --
// it is a second METHOD on the same Service, not a second classifier.
//
// This is §23.1's own "single call site": the ONE place in this codebase
// that ever asks this question. Called ONLY by httpapi.createTurnLocked
// (turn.go), and ONLY after that caller has already confirmed sessionID
// has a plan sitting in plan.StatusAwaitingApproval (§23.1: "gated on 'a
// plan exists and is awaiting_approval' -- the classifier is never invoked
// for this purpose outside that state"). This method itself trusts that
// precondition rather than re-checking it -- Postgres, via createTurnLocked's
// own locked re-verification, is the authority on plan state (§5.1), not
// this method.
//
// text is the reply's own raw, unprefixed body -- a plandomain.RevisePrefix
// -matched reply never reaches this method at all (§23 intro: "the
// revise: prefix stays as a deterministic override that bypasses
// classification entirely"); createTurnLocked only calls this when
// planMode is still false.
//
// NEVER registered on httpapi/wshub as a route of its own (§18.6/§23.4):
// httpapi.createTurnLocked calls this Go method directly, as one internal
// step of a larger, already-authorized operation -- exactly like
// ClassifyAndRecord/RecordDecision already are for the review-vs-request/
// plan-vs-build category. No handler anywhere maps an HTTP route straight
// onto this method.
func (s *Service) ClassifyPlanFollowup(ctx context.Context, text string) ports.IntentDecision {
	logger := platform.Logger(ctx)

	rawTemplate, err := s.templates.GetTemplate(ctx, templateNamePlanFollowup)
	if err != nil {
		logger.Warn("intentclassifier: plan_followup template fetch failed, falling back",
			"fallback_branch", fallbackBranchTemplateFetch, "error", err)
		return fallbackDecision(ports.FallbackReasonAPIError, "")
	}

	// No placeholders: unlike templateNameSystem's own {{surface}}
	// substitution, this category's seeded template references none --
	// createTurnLocked has no "which ingress surface" value of its own to
	// supply here anyway (it is called from every ingress path uniformly).
	// AssembleTemplate(rawTemplate, nil) is a correct, documented no-op
	// whenever tmpl references zero placeholders (template.go's own
	// AssembleTemplate doc comment).
	systemPrompt, err := intentdomain.AssembleTemplate(rawTemplate, nil)
	if err != nil {
		logger.Warn("intentclassifier: plan_followup template assemble failed, falling back",
			"fallback_branch", fallbackBranchTemplateAssemble, "error", err)
		return fallbackDecision(ports.FallbackReasonAPIError, "")
	}

	completion, err := s.llm.Complete(ctx, ports.CompletionRequest{
		Provider:       s.provider,
		Model:          s.model,
		System:         systemPrompt,
		Messages:       []ports.CompletionMessage{{Role: "user", Content: text}},
		ResponseSchema: planFollowupResponseSchema,
	})
	if err != nil {
		logger.Warn("intentclassifier: plan_followup llm call failed, falling back",
			"fallback_branch", fallbackBranchLLMError, "error", err)
		return fallbackFromLLMError(err, "")
	}

	var parsed planFollowupStructuredOutput
	if unmarshalErr := json.Unmarshal(completion.Raw, &parsed); unmarshalErr != nil || !parsed.valid() {
		logger.Warn("intentclassifier: plan_followup llm returned invalid output, falling back",
			"fallback_branch", fallbackBranchInvalidOutput,
			"unmarshal_error", unmarshalErr, "raw_output", string(completion.Raw))
		return fallbackDecision(ports.FallbackReasonInvalidOutput, "")
	}

	// Mirrors Classify's own H9 audit-fix log line one category over:
	// reasoning is deliberately NOT logged (§18.4: stored for audit,
	// never rendered/logged beyond that).
	logger.Info("intentclassifier: plan_followup classified",
		"source", ports.IntentSourceClassifier,
		"target", parsed.Target,
		"confidence", parsed.Confidence,
	)

	return ports.IntentDecision{
		Source:     ports.IntentSourceClassifier,
		Target:     parsed.Target,
		Confidence: parsed.Confidence,
		Reasoning:  intentdomain.TruncateReasoning(parsed.Reasoning),
		CostUSD:    completion.CostUSD,
	}
}
