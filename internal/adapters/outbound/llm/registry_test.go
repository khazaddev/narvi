package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// TestNew_UnsupportedProvider proves the "configure a nonsense provider
// name" fallback branch is real and reachable (§8's own explicit
// requirement), not dead code that could never actually fire because only
// one provider exists: New must never fail to construct, and the
// returned ports.LLM must deterministically fail every Complete call with
// ports.CodeUnsupportedProvider.
func TestNew_UnsupportedProvider(t *testing.T) {
	adapter := New(Config{Provider: "not-a-real-provider", APIKey: "test-api-key"})
	if adapter == nil {
		t.Fatal("New() = nil, want a non-nil ports.LLM even for an unsupported provider")
	}

	_, err := adapter.Complete(context.Background(), ports.CompletionRequest{
		Provider: "not-a-real-provider",
		Model:    "claude-haiku-4-5",
		Messages: []ports.CompletionMessage{{Role: "user", Content: "hello"}},
	})
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
	if llmErr.Provider != "not-a-real-provider" {
		t.Errorf("Provider = %q, want %q", llmErr.Provider, "not-a-real-provider")
	}
}

func TestNew_Anthropic_ReturnsRealAdapter(t *testing.T) {
	adapter := New(Config{Provider: ProviderAnthropic, APIKey: "test-api-key"})
	if _, ok := adapter.(*anthropicAdapter); !ok {
		t.Fatalf("New(Config{Provider: ProviderAnthropic}) = %T, want *anthropicAdapter", adapter)
	}
}
