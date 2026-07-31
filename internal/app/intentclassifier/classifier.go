package intentclassifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/app/ports"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/platform"
)

// The 4 fallback_branch values H9's own audit fix logs on Classify's own
// internal fallback paths -- named exactly as the audit finding itself
// names them, distinct from (and deliberately more granular than) the
// coarse ports.FallbackReason* enum RecordDecision still persists
// unchanged (that enum's own shape is fixed -- TestLLMErrorCode_
// MatchesFallbackReason's own wire-parity guarantee -- so these values
// exist purely as structured-log attributes, never written to Postgres).
const (
	fallbackBranchTemplateFetch    = "template_fetch"
	fallbackBranchTemplateAssemble = "template_assemble"
	fallbackBranchLLMError         = "llm_error"
	fallbackBranchInvalidOutput    = "invalid_output"
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
	// activeSurfaces is §18.5's permanent shadow-vs-active gate, exposed
	// via IsActive: a surface present (true) here is INTENDED to drive
	// real Mode/Target behavior once some future caller actually
	// consults IsActive; any other surface (absent, or explicitly false)
	// is shadow. Honesty note (audit fix, observability/consolidation):
	// as of this batch IsActive has ZERO production callers anywhere in
	// this codebase (confirmed by direct search) -- every decision is
	// still only computed and recorded, never consumed for real
	// behavior, regardless of what this map holds. See IsActive's own
	// doc comment below, and New's own boot-time Warn log. Built once,
	// at construction, from platform.Config.IntentClassifierActiveSurfaces.
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
	// L8 audit fix: told at boot, not left to be discovered the hard way --
	// an operator who sets NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES believes
	// (per IsActive's own, otherwise-accurate doc comment) that this
	// changes real behavior. It does not, yet: nothing in this codebase
	// calls IsActive outside this package's own tests. No ctx is available
	// this early (construction happens once, at process boot, before any
	// request/session scope exists), so this logs via the process-wide
	// default logger directly, exactly like cmd/control-plane/main.go's own
	// other boot-time log lines.
	if len(active) > 0 {
		slog.Warn("intentclassifier: active surfaces configured, but IsActive has no production caller yet -- this setting currently has zero effect on real routing/behavior",
			"active_surfaces", activeSurfaces)
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
//
// Honesty note (audit fix, observability/consolidation): the gate itself
// is real and correctly built, but this method currently has NO
// production caller anywhere in this codebase -- nothing yet consults
// IsActive to change real behavior for any surface, active or shadow.
// Building the actual active-mode consumer (some caller that branches on
// this to act on Target/Mode instead of only recording them) is a
// separate, later decision, deliberately out of this batch's own scope.
// New's own boot-time Warn log tells an operator this directly the moment
// they configure a non-empty active-surfaces list.
func (s *Service) IsActive(surface string) bool {
	return s.activeSurfaces[surface]
}

// Classify implements ports.IntentClassifier (§18.1's never-throw
// contract): every code path below resolves to a returned
// ports.IntentDecision, never a caller-fatal error.
func (s *Service) Classify(ctx context.Context, input ports.IntentClassifierInput) ports.IntentDecision {
	logger := platform.Logger(ctx)

	rawTemplate, err := s.templates.GetTemplate(ctx, templateNameSystem)
	if err != nil {
		logger.Warn("intentclassifier: template fetch failed, falling back",
			"fallback_branch", fallbackBranchTemplateFetch, "surface", input.Surface, "error", err)
		return fallbackDecision(ports.FallbackReasonAPIError, input.DeterministicTarget)
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
		logger.Warn("intentclassifier: template assemble failed, falling back",
			"fallback_branch", fallbackBranchTemplateAssemble, "surface", input.Surface, "error", err)
		return fallbackDecision(ports.FallbackReasonAPIError, input.DeterministicTarget)
	}

	completion, err := s.llm.Complete(ctx, ports.CompletionRequest{
		Provider:       s.provider,
		Model:          s.model,
		System:         systemPrompt,
		Messages:       []ports.CompletionMessage{{Role: "user", Content: input.Text}},
		ResponseSchema: responseSchema,
	})
	if err != nil {
		// err's own Error() string already embeds LLMError's Code/
		// Provider/wrapped-Err trio (ports/llm.go) when the ports.LLM
		// implementation honors its own contract -- logged here
		// unconditionally (never re-typed), so this line stays correct
		// even for the "non-typed error, itself a bug in that LLM
		// implementation" case fallbackFromLLMError below still handles
		// gracefully.
		logger.Warn("intentclassifier: llm call failed, falling back",
			"fallback_branch", fallbackBranchLLMError, "surface", input.Surface, "error", err)
		return fallbackFromLLMError(err, input.DeterministicTarget)
	}

	var parsed structuredOutput
	if unmarshalErr := json.Unmarshal(completion.Raw, &parsed); unmarshalErr != nil || !parsed.valid() {
		logger.Warn("intentclassifier: llm returned invalid output, falling back",
			"fallback_branch", fallbackBranchInvalidOutput, "surface", input.Surface,
			"unmarshal_error", unmarshalErr, "raw_output", string(completion.Raw))
		return fallbackDecision(ports.FallbackReasonInvalidOutput, input.DeterministicTarget)
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

	// H9 audit fix: a genuine classifier verdict was, until this batch,
	// invisible in the logs entirely -- an operator had nothing beyond
	// the persisted decision record itself to confirm what was
	// classified and why. Info, not Warn/Debug: this fires once per
	// session across every surface, exactly matching the log-volume/
	// level this codebase already uses for other once-per-session events
	// (e.g. github's own "created new review session for mention").
	// Reasoning itself is deliberately NOT logged here (unlike target/
	// mode/confidence/source) -- it is already persisted to the decision
	// record for audit, and §18.4 explicitly bars rendering it on any
	// ingress-facing surface by default; logging is a smaller, but still
	// real, exposure surface than doing so purely for observability buys.
	logger.Info("intentclassifier: classified",
		"surface", input.Surface,
		"has_deterministic_target", input.DeterministicTarget != "",
		"source", ports.IntentSourceClassifier,
		"target", target,
		"mode", parsed.Mode,
		"confidence", confidence,
	)

	return ports.IntentDecision{
		Source:     ports.IntentSourceClassifier,
		Target:     target,
		Mode:       parsed.Mode,
		Confidence: confidence,
		Reasoning:  intentdomain.TruncateReasoning(parsed.Reasoning),
		// L18 audit fix: the real cost of THIS call, when the underlying
		// ports.LLM implementation could compute one -- nil (never
		// guessed) when it couldn't (see CompletionResponse.CostUSD's own
		// doc comment).
		CostUSD: completion.CostUSD,
	}
}

// fallbackDecision builds the fixed fallback shape (§18.1) for reason.
//
// deterministicTarget (Step 47, §5.2/§18.2 audit fix) is threaded through
// to the returned decision's own Target field -- BEFORE this fix,
// fallbackDecision unconditionally left Target empty on every fallback
// path, discarding a caller-supplied deterministic signal (a regex/label
// match, §18.2's own "independent deterministic check") even when one was
// available. §5.2's own words state the corollary this was silently
// failing: "Any re-run/re-review phrasing a posted verdict... recommends
// to a user... must be recognizable by the intent classifier's
// deterministic fail-open fallback... not only by its model-based path."
// A caller that already has a structural, non-LLM answer for Target
// (GitHub's ingress always does -- DeterministicTarget: intentdomain.
// TargetReview, coalesce.go's own doc comment: "already deterministically
// means this mention landed on a pull request") must have that answer
// survive EVERY failure mode of the model-based path above (template
// fetch/assemble failure, an LLM error, invalid output) -- not just the
// happy path's own corroboration step (which only ever runs after a
// successful LLM call). An empty deterministicTarget (no independent
// signal available for this input -- Slack/Linear's own current callers,
// which supply none) leaves Target exactly as empty as before this fix.
func fallbackDecision(reason string, deterministicTarget string) ports.IntentDecision {
	return ports.IntentDecision{
		Source:         ports.IntentSourceFallback,
		FallbackReason: reason,
		Target:         deterministicTarget,
	}
}

// fallbackFromLLMError maps a ports.LLM.Complete failure onto
// IntentDecision.FallbackReason -- via the *ports.LLMError.Code TYPED
// error, never by string-matching err's own message (§18.1).
// LLMErrorCode's five values and FallbackReason's five values are the
// SAME strings (ports/llm_test.go's own TestLLMErrorCode_MatchesFallbackReason
// locks this), so this is a direct 1:1 mapping. deterministicTarget is
// threaded straight through to fallbackDecision -- see that function's own
// doc comment.
func fallbackFromLLMError(err error, deterministicTarget string) ports.IntentDecision {
	var llmErr *ports.LLMError
	if errors.As(err, &llmErr) {
		return fallbackDecision(string(llmErr.Code), deterministicTarget)
	}
	// A ports.LLM implementation that returns a non-*ports.LLMError is
	// itself a bug in that implementation (the port's own contract
	// requires typed errors) -- fall back to the generic api_error
	// reason rather than propagate, per §18.1's never-throw contract,
	// never panicking over another package's own bug.
	return fallbackDecision(ports.FallbackReasonAPIError, deterministicTarget)
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

// ClassifyAndRecord is the H9/L11 audit fix's shared classify+record step
// (§8.3/§18): calls Classify, derives the Confidence/Reasoning pointers
// RecordDecision needs (populated only for a genuine ports.
// IntentSourceClassifier verdict, nil for a fallback -- exactly §18.4's
// own nullability rule), and persists the resulting intentdomain.
// IntentDecisionRecord write-once via RecordDecision -- replacing the
// near-identical block previously copy-pasted, verbatim, across github/
// coalesce.go, slack/handler.go, and linear/webhook.go.
//
// input.Surface doubles as both ports.IntentClassifierInput.Surface AND
// intentdomain.IntentDecisionRecord.Surface (the same value every
// existing call site already passed identically to both), and also as
// this call's own log-line prefix on a RecordDecision failure ("github: "/
// "slack: "/"linear: ", exactly matching each site's own pre-existing Warn
// text) -- so no separate surface-prefix parameter is needed. stage is the
// one genuine remaining per-caller difference (intentdomain.
// DecidedAtStageCreate for GitHub/Linear, DecidedAtStageFirstPrompt for
// Slack); input.DeterministicTarget (GitHub's own real, deterministic
// signal; empty for Slack/Linear) is the other, and it is simply part of
// input, exactly like every other IntentClassifierInput field -- callers
// set it before calling, same as they already do for Classify directly.
//
// Fire-and-forget in production (no caller today does anything with the
// returned ports.IntentDecision beyond what RecordDecision already
// persisted -- confirmed by reading all 3 pre-existing call sites): the
// decision is still returned rather than discarded, purely so this
// method itself stays directly unit-testable without needing to poll a
// session row back out of a store fake.
//
// Runs entirely OUTSIDE any Postgres transaction, exactly like every
// pre-existing call site already did (a real outbound LLM call must never
// hold one open) -- callers are unaffected by this consolidation.
func (s *Service) ClassifyAndRecord(ctx context.Context, sessionID pgtype.UUID, input ports.IntentClassifierInput, stage string) ports.IntentDecision {
	logger := platform.Logger(ctx)

	decision := s.Classify(ctx, input)

	var confidence, reasoning *string
	if decision.Source == ports.IntentSourceClassifier {
		confVal := decision.Confidence
		confidence = &confVal
		// decision.Reasoning already went through intentdomain.
		// TruncateReasoning inside Classify itself -- truncating again
		// here would be redundant (TruncateReasoning is idempotent on an
		// already-bounded string, but there is no reason to call it
		// twice).
		reasonVal := decision.Reasoning
		reasoning = &reasonVal
	}

	if _, err := s.RecordDecision(ctx, sessionID, intentdomain.IntentDecisionRecord{
		Surface:        input.Surface,
		Source:         decision.Source,
		Target:         decision.Target,
		Mode:           decision.Mode,
		Confidence:     confidence,
		Reasoning:      reasoning,
		DecidedAt:      time.Now(),
		DecidedAtStage: stage,
		// L18 audit fix: carried straight across from decision.CostUSD --
		// already nil for every fallback decision (Classify never
		// populates it on that path), so no separate Source check is
		// needed here the way Confidence/Reasoning above require one.
		CostUSD: decision.CostUSD,
	}); err != nil {
		// Never fatal to the caller -- mirrors every pre-existing call
		// site's own identical "log and otherwise ignore" handling: the
		// session/turn/acknowledgment this decision rides along with is
		// already fully created/dispatched by the time any caller reaches
		// this method.
		logger.Warn(input.Surface+": record intent decision failed", "error", err, "session_id", sessionID)
	}

	return decision
}
