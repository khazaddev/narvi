package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/khazaddev/narvi/internal/app/ports"
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
func (a *anthropicAdapter) Complete(ctx context.Context, req ports.CompletionRequest) (json.RawMessage, error) {
	if a.misconfigured != nil {
		// Copy, not the shared pointer -- Provider below reflects THIS
		// request's own req.Provider for logging, even though the
		// underlying misconfiguration is identical every time.
		err := *a.misconfigured
		if req.Provider != "" {
			err.Provider = req.Provider
		}
		return nil, &err
	}

	model, ok := supportedModels[req.Model]
	if !ok {
		return nil, &ports.LLMError{
			Code:     ports.CodeUnsupportedProvider,
			Provider: req.Provider,
			Err:      fmt.Errorf("llm: unrecognized anthropic model %q", req.Model),
		}
	}

	var schema map[string]any
	if len(req.ResponseSchema) > 0 {
		if err := json.Unmarshal(req.ResponseSchema, &schema); err != nil {
			return nil, &ports.LLMError{
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
		return nil, classifyCompletionError(req.Provider, err)
	}

	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			if !json.Valid([]byte(tb.Text)) {
				return nil, &ports.LLMError{
					Code:     ports.CodeInvalidOutput,
					Provider: req.Provider,
					Err:      fmt.Errorf("llm: response text is not valid JSON (stop_reason=%s)", resp.StopReason),
				}
			}
			return json.RawMessage(tb.Text), nil
		}
	}

	return nil, &ports.LLMError{
		Code:     ports.CodeInvalidOutput,
		Provider: req.Provider,
		Err:      fmt.Errorf("llm: response contained no text content block (stop_reason=%s)", resp.StopReason),
	}
}
