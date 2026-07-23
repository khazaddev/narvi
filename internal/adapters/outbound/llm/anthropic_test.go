package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
)

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
	if string(got) != wantJSON {
		t.Errorf("Complete() = %s, want %s", got, wantJSON)
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
