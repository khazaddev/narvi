package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/app/ports"
)

// captureDefaultLoggerJSON temporarily replaces slog.Default() with a JSON
// handler writing into a *bytes.Buffer, restoring the original on
// cleanup -- mirrors internal/app/intentclassifier/logging_test.go's own
// identical helper (Complete logs via platform.Logger(ctx), which resolves
// slog.Default() when ctx carries no correlation id, exactly as here).
func captureDefaultLoggerJSON(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(origLogger) })
	return &buf
}

const testResponseSchema = `{
	"type": "object",
	"properties": {
		"target": {"type": "string", "enum": ["review", "request"]},
		"mode": {"type": "string", "enum": ["plan", "build"]},
		"confidence": {"type": "string", "enum": ["high", "medium", "low"]},
		"reasoning": {"type": "string"}
	},
	"required": ["target", "mode", "confidence", "reasoning"],
	"additionalProperties": false
}`

func testRequest() ports.CompletionRequest {
	return ports.CompletionRequest{
		Provider:       ProviderAnthropic,
		Model:          "claude-haiku-4-5",
		System:         "you are a classifier",
		Messages:       []ports.CompletionMessage{{Role: "user", Content: "please review this PR"}},
		ResponseSchema: json.RawMessage(testResponseSchema),
	}
}

func anthropicMessageResponse(text string) string {
	body := map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"model":         "claude-haiku-4-5",
		"content":       []map[string]any{{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 10, "output_tokens": 20},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestAnthropicAdapter_Complete_Success(t *testing.T) {
	wantJSON := `{"target":"review","mode":"build","confidence":"high","reasoning":"a direct ask to review"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Errorf("x-api-key header = %q, want %q", r.Header.Get("x-api-key"), "test-api-key")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageResponse(wantJSON)))
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Second,
	})

	got, err := adapter.Complete(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v, want nil", err)
	}
	if string(got.Raw) != wantJSON {
		t.Errorf("Complete().Raw = %s, want %s", got.Raw, wantJSON)
	}
}

// TestAnthropicAdapter_Complete_Success_ReportsUsageAndCost is the L18
// audit fix's own adapter-level coverage: a real (fake-HTTP-server-backed)
// Anthropic response carrying a known usage block decodes into the
// correct CompletionUsage AND the correct CompletionResponse.CostUSD, for
// a known, real model+token-count combination, using the REAL, verified
// per-model pricing figures (see modelPricing's own doc comment in
// anthropic.go for how those were verified) -- not invented numbers.
//
// anthropicMessageResponse's own fixture body (used by every other test in
// this file too) already carries "usage": {"input_tokens": 10,
// "output_tokens": 20} -- testRequest's own Model is "claude-haiku-4-5",
// priced at $1.00/1M input tokens and $5.00/1M output tokens. The expected
// cost, worked by hand so a reader can verify it directly:
//
//	input:  10 tokens / 1,000,000 * $1.00 = $0.00001
//	output: 20 tokens / 1,000,000 * $5.00 = $0.00010
//	total:                                  $0.00011
func TestAnthropicAdapter_Complete_Success_ReportsUsageAndCost(t *testing.T) {
	wantJSON := `{"target":"review","mode":"build","confidence":"high","reasoning":"a direct ask to review"}`
	const wantCost = 0.00011

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageResponse(wantJSON)))
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Second,
	})

	got, err := adapter.Complete(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v, want nil", err)
	}

	if got.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens = %d, want 10", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens != 20 {
		t.Errorf("Usage.OutputTokens = %d, want 20", got.Usage.OutputTokens)
	}
	if got.CostUSD == nil {
		t.Fatal("CostUSD = nil, want a computed cost for a recognized model")
	}
	if diff := *got.CostUSD - wantCost; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CostUSD = %v, want %v (see this test's own doc comment for the arithmetic)", *got.CostUSD, wantCost)
	}
}

// TestAnthropicAdapter_Complete_UnrecognizedPricingModel_NoCostComputed is
// this test's own sibling coverage for the "genuinely new/renamed model"
// gap L18's own fix guards against: a model recognized by supportedModels
// (so the real API call still succeeds) but, by construction here, absent
// from modelPricing -- CostUSD must stay nil (never a guessed figure), a
// Warn must be logged identifying the model, and Complete must not panic.
//
// Temporarily deletes "claude-haiku-4-5"'s own modelPricing entry (restored
// via t.Cleanup) rather than inventing a model string supportedModels
// itself doesn't recognize -- that would hit the EARLIER
// CodeUnsupportedProvider branch instead (TestAnthropicAdapter_Complete_
// UnrecognizedModel above), a different, already-covered code path. This
// test targets the LATER, pricing-only gap: a model the adapter can and
// does complete a real call for, that its own pricing table doesn't know.
func TestAnthropicAdapter_Complete_UnrecognizedPricingModel_NoCostComputed(t *testing.T) {
	const model = "claude-haiku-4-5"
	origPrice, hadPrice := modelPricing[model]
	delete(modelPricing, model)
	t.Cleanup(func() {
		if hadPrice {
			modelPricing[model] = origPrice
		}
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageResponse(`{"target":"review","mode":"build","confidence":"high","reasoning":"x"}`)))
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Second,
	})

	buf := captureDefaultLoggerJSON(t)

	got, err := adapter.Complete(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v, want nil (a missing PRICING entry is not a call failure)", err)
	}
	if got.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil for a model absent from modelPricing", *got.CostUSD)
	}
	// Usage itself is still real and populated -- only the priced dollar
	// figure is unavailable.
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 20 {
		t.Errorf("Usage = %+v, want InputTokens=10 OutputTokens=20 (usage itself is unaffected by a pricing gap)", got.Usage)
	}

	var found bool
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if entry["level"] == "WARN" && entry["model"] == model {
			found = true
		}
	}
	if !found {
		t.Errorf("no WARN log entry naming model %q found in log output: %s", model, buf.String())
	}
}

func TestAnthropicAdapter_Complete_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageResponse(`{"target":"review","mode":"build","confidence":"high","reasoning":"x"}`)))
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Millisecond,
	})

	_, err := adapter.Complete(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Complete() error = nil, want a timeout error")
	}
	var llmErr *ports.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not *ports.LLMError: %v (%T)", err, err)
	}
	if llmErr.Code != ports.CodeTimeout {
		t.Errorf("Code = %q, want %q", llmErr.Code, ports.CodeTimeout)
	}
}

func TestAnthropicAdapter_Complete_InvalidOutput_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(anthropicMessageResponse(`this is not valid JSON at all`)))
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Second,
	})

	_, err := adapter.Complete(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Complete() error = nil, want an invalid_output error")
	}
	var llmErr *ports.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not *ports.LLMError: %v (%T)", err, err)
	}
	if llmErr.Code != ports.CodeInvalidOutput {
		t.Errorf("Code = %q, want %q", llmErr.Code, ports.CodeInvalidOutput)
	}
}

func TestAnthropicAdapter_Complete_InvalidOutput_NoTextBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]any{
			"id":            "msg_test",
			"type":          "message",
			"role":          "assistant",
			"model":         "claude-haiku-4-5",
			"content":       []map[string]any{},
			"stop_reason":   "max_tokens",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 10, "output_tokens": 0},
		}
		raw, _ := json.Marshal(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Second,
	})

	_, err := adapter.Complete(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Complete() error = nil, want an invalid_output error")
	}
	var llmErr *ports.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not *ports.LLMError: %v (%T)", err, err)
	}
	if llmErr.Code != ports.CodeInvalidOutput {
		t.Errorf("Code = %q, want %q", llmErr.Code, ports.CodeInvalidOutput)
	}
}

func TestAnthropicAdapter_Complete_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"internal server error"}}`))
	}))
	defer server.Close()

	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
		BaseURL:  server.URL,
		Timeout:  5 * time.Second,
	})

	_, err := adapter.Complete(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Complete() error = nil, want an api_error")
	}
	var llmErr *ports.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not *ports.LLMError: %v (%T)", err, err)
	}
	if llmErr.Code != ports.CodeAPIError {
		t.Errorf("Code = %q, want %q", llmErr.Code, ports.CodeAPIError)
	}
}

func TestAnthropicAdapter_Complete_NoAPIKey(t *testing.T) {
	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "",
	})

	_, err := adapter.Complete(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Complete() error = nil, want a no_api_key error")
	}
	var llmErr *ports.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not *ports.LLMError: %v (%T)", err, err)
	}
	if llmErr.Code != ports.CodeNoAPIKey {
		t.Errorf("Code = %q, want %q", llmErr.Code, ports.CodeNoAPIKey)
	}

	// Deterministic: a second call fails the exact same way, with no
	// network call ever attempted (New was constructed with no BaseURL at
	// all, so any real attempt would fail differently/hang).
	_, err2 := adapter.Complete(context.Background(), testRequest())
	var llmErr2 *ports.LLMError
	if !errors.As(err2, &llmErr2) || llmErr2.Code != ports.CodeNoAPIKey {
		t.Errorf("second Complete() call did not deterministically repeat CodeNoAPIKey: %v", err2)
	}
}

func TestAnthropicAdapter_Complete_UnrecognizedModel(t *testing.T) {
	adapter := New(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-api-key",
	})

	req := testRequest()
	req.Model = "not-a-real-model"

	_, err := adapter.Complete(context.Background(), req)
	if err == nil {
		t.Fatal("Complete() error = nil, want an unsupported_provider error")
	}
	var llmErr *ports.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not *ports.LLMError: %v (%T)", err, err)
	}
	if llmErr.Code != ports.CodeUnsupportedProvider {
		t.Errorf("Code = %q, want %q", llmErr.Code, ports.CodeUnsupportedProvider)
	}
}
