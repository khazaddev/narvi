package findingposition

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/narvidev/narvi/internal/app/ports"
)

type fakeLLM struct {
	response json.RawMessage
	err      error
	calls    int
}

func (f *fakeLLM) Complete(_ context.Context, _ ports.CompletionRequest) (ports.CompletionResponse, error) {
	f.calls++
	if f.err != nil {
		return ports.CompletionResponse{}, f.err
	}
	return ports.CompletionResponse{Raw: f.response}, nil
}

func TestResolver_Resolve_FoundReturnsLineRange(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":42,"endLine":44}`)}
	r := New(llm, "anthropic", "claude-haiku-4-5")

	startLine, endLine := r.Resolve(context.Background(), "main.go", "some finding", "some diff")
	if startLine != 42 || endLine != 44 {
		t.Errorf("Resolve() = (%d, %d), want (42, 44)", startLine, endLine)
	}
	if llm.calls != 1 {
		t.Errorf("Complete called %d times, want 1", llm.calls)
	}
}

func TestResolver_Resolve_LLMErrorDegradesToZero(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{err: &ports.LLMError{Code: ports.CodeTimeout, Provider: "anthropic"}}
	r := New(llm, "anthropic", "claude-haiku-4-5")

	startLine, endLine := r.Resolve(context.Background(), "main.go", "some finding", "some diff")
	if startLine != 0 || endLine != 0 {
		t.Errorf("Resolve() = (%d, %d), want (0, 0) on LLM error", startLine, endLine)
	}
}

func TestResolver_Resolve_InvalidJSONDegradesToZero(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`not json at all`)}
	r := New(llm, "anthropic", "claude-haiku-4-5")

	startLine, endLine := r.Resolve(context.Background(), "main.go", "some finding", "some diff")
	if startLine != 0 || endLine != 0 {
		t.Errorf("Resolve() = (%d, %d), want (0, 0) on invalid JSON", startLine, endLine)
	}
}

func TestResolver_Resolve_FoundFalseDegradesToZero(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":false,"startLine":0,"endLine":0}`)}
	r := New(llm, "anthropic", "claude-haiku-4-5")

	startLine, endLine := r.Resolve(context.Background(), "main.go", "some finding", "some diff")
	if startLine != 0 || endLine != 0 {
		t.Errorf("Resolve() = (%d, %d), want (0, 0) when found=false", startLine, endLine)
	}
}

func TestResolver_Resolve_FoundTrueButZeroStartLineIsInvalid(t *testing.T) {
	t.Parallel()

	// A provider reporting found=true with startLine=0 violates this
	// package's own "0 is never a value a found=true result may report"
	// contract -- treated as invalid output, never trusted.
	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":0,"endLine":5}`)}
	r := New(llm, "anthropic", "claude-haiku-4-5")

	startLine, endLine := r.Resolve(context.Background(), "main.go", "some finding", "some diff")
	if startLine != 0 || endLine != 0 {
		t.Errorf("Resolve() = (%d, %d), want (0, 0) for an invalid found=true/startLine=0 response", startLine, endLine)
	}
}

func TestResolver_Resolve_FoundTrueButInvertedRangeIsInvalid(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":10,"endLine":5}`)}
	r := New(llm, "anthropic", "claude-haiku-4-5")

	startLine, endLine := r.Resolve(context.Background(), "main.go", "some finding", "some diff")
	if startLine != 0 || endLine != 0 {
		t.Errorf("Resolve() = (%d, %d), want (0, 0) for an inverted endLine < startLine response", startLine, endLine)
	}
}

func TestStructuredOutput_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  structuredOutput
		want bool
	}{
		{"found false, any lines", structuredOutput{Found: false, StartLine: 0, EndLine: 0}, true},
		{"found true, valid single line", structuredOutput{Found: true, StartLine: 5, EndLine: 5}, true},
		{"found true, valid range", structuredOutput{Found: true, StartLine: 5, EndLine: 10}, true},
		{"found true, zero start", structuredOutput{Found: true, StartLine: 0, EndLine: 5}, false},
		{"found true, negative start", structuredOutput{Found: true, StartLine: -1, EndLine: 5}, false},
		{"found true, end before start", structuredOutput{Found: true, StartLine: 10, EndLine: 5}, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.out.valid(); got != tc.want {
				t.Errorf("valid() = %v, want %v", got, tc.want)
			}
		})
	}
}
