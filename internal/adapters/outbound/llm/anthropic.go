package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/platform"
)

// maxCompletionTokens bounds every classification request's own
// max_tokens. Not specified in the plan; chosen as 1024 -- generous for a
// structured-output response containing only target/mode/confidence/
// reasoning (a few short fields, no free-form generation), while staying
// far under the ~16K threshold where the Anthropic SDK itself requires
// streaming to avoid an HTTP timeout (this call never streams).
const maxCompletionTokens = 1024

// supportedModels is the finite set of model identifiers this adapter
// recognizes, mapped to the SDK's own typed Model constants. An
// unrecognized/misconfigured NARVI_INTENT_CLASSIFIER_MODEL value is a
// real, reachable *ports.LLMError{Code: ports.CodeUnsupportedProvider}
// path (§18.1) — never a silent substitution. Restricted to models the
// Anthropic API documents as supporting structured outputs
// (output_config.format) at the time this Step was written.
var supportedModels = map[string]anthropic.Model{
	"claude-haiku-4-5":          anthropic.ModelClaudeHaiku4_5,
	"claude-haiku-4-5-20251001": anthropic.ModelClaudeHaiku4_5_20251001,
	"claude-sonnet-5":           anthropic.ModelClaudeSonnet5,
	"claude-sonnet-4-6":         anthropic.ModelClaudeSonnet4_6,
	"claude-opus-4-8":           anthropic.ModelClaudeOpus4_8,
	"claude-opus-4-7":           anthropic.ModelClaudeOpus4_7,
	"claude-opus-4-6":           anthropic.ModelClaudeOpus4_6,
	"claude-opus-4-5":           anthropic.ModelClaudeOpus4_5,
	"claude-opus-4-5-20251101":  anthropic.ModelClaudeOpus4_5_20251101,
	"claude-fable-5":            anthropic.ModelClaudeFable5,
}

// modelPrice is one modelPricing entry: $/1,000,000 tokens, input and
// output priced separately (every Anthropic model's own published rate
// card shape).
type modelPrice struct {
	inputPerMTok  float64
	outputPerMTok float64
}

// modelPricing is this adapter's own small, server-side, per-model pricing
// table (audit fix L18) used to compute CompletionResponse.CostUSD.
// Provider-specific pricing is inherently vendor-specific business logic --
// it must never leak into internal/app/ports (that port's own explicit
// "nothing Anthropic-specific may leak into this file" rule), so this table
// lives here, in the one adapter that knows both "which model" and "what
// Anthropic charges for it".
//
// Figures verified directly against Anthropic's own published pricing
// (platform.claude.com/docs/en/about-claude/models/overview.md, "Latest
// models comparison" + "Legacy models" tables) on 2026-07-29 -- not
// estimated, not carried over from training-time recall. claude-opus-4-5
// in particular does not appear in some cached/quick-reference pricing
// summaries; its $5/$25 figure below was looked up directly from that same
// live page's "Legacy models" table (claude-opus-4-5-20251101 row) rather
// than guessed.
//
// claude-sonnet-5 is priced here at its CURRENT, LIVE $2/$10 introductory
// rate -- Anthropic is actually billing $2/$10 (not the $3/$15 standard
// rate) through 2026-08-31, and this table must reflect what is actually
// being charged today, not the rate that resumes afterward. This entry
// goes stale automatically on 2026-08-31: revert it to the standard
// $3.00 input / $15.00 output per-million-token rate once that
// introductory window ends (verify the standing rate is still $3/$15 at
// that point rather than assuming it).
//
// Every key here mirrors a supportedModels key above (both the dated and
// undated aliases for haiku-4-5/opus-4-5) -- but the two maps are
// DELIBERATELY independent, not derived from one another: a model added to
// supportedModels without a matching entry here is a real, reachable,
// tested gap (costUSD below returns ok=false; Complete logs a Warn and
// leaves CompletionResponse.CostUSD nil) rather than a compile-time
// guarantee -- precisely so a future model addition that forgets pricing
// fails safe (no cost recorded, loudly logged) instead of silently
// mis-costing or panicking.
var modelPricing = map[string]modelPrice{
	"claude-haiku-4-5":          {inputPerMTok: 1.00, outputPerMTok: 5.00},
	"claude-haiku-4-5-20251001": {inputPerMTok: 1.00, outputPerMTok: 5.00},
	"claude-sonnet-5":           {inputPerMTok: 2.00, outputPerMTok: 10.00},
	"claude-sonnet-4-6":         {inputPerMTok: 3.00, outputPerMTok: 15.00},
	"claude-opus-4-8":           {inputPerMTok: 5.00, outputPerMTok: 25.00},
	"claude-opus-4-7":           {inputPerMTok: 5.00, outputPerMTok: 25.00},
	"claude-opus-4-6":           {inputPerMTok: 5.00, outputPerMTok: 25.00},
	"claude-opus-4-5":           {inputPerMTok: 5.00, outputPerMTok: 25.00},
	"claude-opus-4-5-20251101":  {inputPerMTok: 5.00, outputPerMTok: 25.00},
	"claude-fable-5":            {inputPerMTok: 10.00, outputPerMTok: 50.00},
}

// costUSD computes the dollar cost of usage against model (req.Model's own
// raw string -- the SAME key space supportedModels uses, never the SDK's
// own typed anthropic.Model value). ok is false, and cost is meaningless,
// whenever model has no modelPricing entry: the caller must treat that as
// "no cost available" and leave CompletionResponse.CostUSD nil -- never
// fall back to a guessed figure (mirrors that field's own "omitted, never
// guessed" contract).
func costUSD(model string, usage ports.CompletionUsage) (cost float64, ok bool) {
	price, ok := modelPricing[model]
	if !ok {
		return 0, false
	}
	cost = float64(usage.InputTokens)/1_000_000*price.inputPerMTok +
		float64(usage.OutputTokens)/1_000_000*price.outputPerMTok
	return cost, true
}

// anthropicAdapter implements ports.LLM against the real Anthropic
// Messages API, via github.com/anthropics/anthropic-sdk-go. Deliberately
// NOT bound to a single fixed model at construction -- ports.
// CompletionRequest.Model is a genuine PER-CALL parameter (§4.3: this
// port is shaped for reuse by the future model catalog/code-review work
// too, which may pick a different model per call through the SAME
// client), so model recognition happens inside Complete, against
// req.Model, every call.
type anthropicAdapter struct {
	client anthropic.Client
	// misconfigured is set at construction whenever this adapter can
	// never succeed for a structural reason (no API key) -- every
	// Complete call then deterministically returns the SAME typed error,
	// forever, rather than attempting (and failing) a real network call
	// every time.
	misconfigured *ports.LLMError
}

var _ ports.LLM = (*anthropicAdapter)(nil)

// newAnthropicAdapter builds the real Anthropic ports.LLM implementation.
// NEVER fails to construct — an empty cfg.APIKey is recorded as a
// permanent, reachable failure mode on the returned value instead (see
// anthropicAdapter.misconfigured), matching §18.1's own "never a silent
// substitution" rule while keeping process boot itself resilient to a
// misconfigured internal classification feature.
func newAnthropicAdapter(cfg Config) *anthropicAdapter {
	if cfg.APIKey == "" {
		return &anthropicAdapter{
			misconfigured: &ports.LLMError{
				Code:     ports.CodeNoAPIKey,
				Provider: ProviderAnthropic,
				Err:      errors.New("llm: no Anthropic API key configured"),
			},
		}
	}

	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		// The ONLY timeout layer for this call (§18.1) — configured once,
		// here, on the client itself; Complete below never additionally
		// races a manually-armed context.WithTimeout against it.
		opts = append(opts, option.WithRequestTimeout(cfg.Timeout))
	}

	return &anthropicAdapter{
		client: anthropic.NewClient(opts...),
	}
}

// Complete implements ports.LLM. Uses the SDK's own structured-output
// feature (output_config.format, a JSON schema forcing the response
// shape) rather than free-text JSON + hand-parsing, and deliberately
// never sets Thinking — intent classification is a fast, low-complexity,
// high-volume, latency-sensitive call, not a "remotely complicated"
// reasoning task (§18.1).
//
// Audit fix L18: on success, also reads the real usage block every
// Anthropic Messages API response carries (resp.Usage.InputTokens/
// OutputTokens — verified against the actual pinned SDK version this
// module depends on, github.com/anthropics/anthropic-sdk-go, not assumed)
// and computes CompletionResponse.CostUSD from modelPricing above. Usage/
// CostUSD are new; every existing behavior (the raw JSON returned, every
// error path/Code) is unchanged.
func (a *anthropicAdapter) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	if a.misconfigured != nil {
		// Copy, not the shared pointer -- Provider below reflects THIS
		// request's own req.Provider for logging, even though the
		// underlying misconfiguration is identical every time.
		err := *a.misconfigured
		if req.Provider != "" {
			err.Provider = req.Provider
		}
		return ports.CompletionResponse{}, &err
	}

	model, ok := supportedModels[req.Model]
	if !ok {
		return ports.CompletionResponse{}, &ports.LLMError{
			Code:     ports.CodeUnsupportedProvider,
			Provider: req.Provider,
			Err:      fmt.Errorf("llm: unrecognized anthropic model %q", req.Model),
		}
	}

	var schema map[string]any
	if len(req.ResponseSchema) > 0 {
		if err := json.Unmarshal(req.ResponseSchema, &schema); err != nil {
			return ports.CompletionResponse{}, &ports.LLMError{
				Code:     ports.CodeInvalidOutput,
				Provider: req.Provider,
				Err:      fmt.Errorf("llm: ResponseSchema is not valid JSON: %w", err),
			}
		}
	}

	messages := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		block := anthropic.NewTextBlock(m.Content)
		if m.Role == "assistant" {
			messages = append(messages, anthropic.NewAssistantMessage(block))
			continue
		}
		messages = append(messages, anthropic.NewUserMessage(block))
	}

	params := anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxCompletionTokens,
		Messages:  messages,
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	resp, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return ports.CompletionResponse{}, classifyCompletionError(req.Provider, err)
	}

	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			if !json.Valid([]byte(tb.Text)) {
				return ports.CompletionResponse{}, &ports.LLMError{
					Code:     ports.CodeInvalidOutput,
					Provider: req.Provider,
					Err:      fmt.Errorf("llm: response text is not valid JSON (stop_reason=%s)", resp.StopReason),
				}
			}

			usage := ports.CompletionUsage{
				InputTokens:  resp.Usage.InputTokens,
				OutputTokens: resp.Usage.OutputTokens,
			}
			var costPtr *float64
			if cost, ok := costUSD(req.Model, usage); ok {
				costPtr = &cost
			} else {
				// L18 audit fix: a model recognized by supportedModels
				// above but missing from modelPricing is a real,
				// reachable maintenance gap (a new/renamed model added to
				// one table but not the other) -- surfaced loudly here so
				// it gets fixed, rather than silently reporting a wrong
				// (zero, or worse, stale) cost forever. CostUSD stays nil
				// (never guessed) on this path.
				platform.Logger(ctx).Warn("llm: no pricing entry for model, cost not recorded",
					"provider", req.Provider, "model", req.Model)
			}

			return ports.CompletionResponse{
				Raw:     json.RawMessage(tb.Text),
				Usage:   usage,
				CostUSD: costPtr,
			}, nil
		}
	}

	return ports.CompletionResponse{}, &ports.LLMError{
		Code:     ports.CodeInvalidOutput,
		Provider: req.Provider,
		Err:      fmt.Errorf("llm: response contained no text content block (stop_reason=%s)", resp.StopReason),
	}
}
