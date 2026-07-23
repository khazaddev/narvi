package ports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLLMError_Error(t *testing.T) {
	sentinel := errors.New("connection refused")

	tests := []struct {
		name string
		err  *LLMError
		want []string
	}{
		{
			name: "with wrapped error",
			err:  &LLMError{Code: CodeAPIError, Provider: "anthropic", Err: sentinel},
			want: []string{string(CodeAPIError), "anthropic", "connection refused"},
		},
		{
			name: "no wrapped error",
			err:  &LLMError{Code: CodeNoAPIKey, Provider: "anthropic"},
			want: []string{string(CodeNoAPIKey), "anthropic"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("Error() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestLLMError_Unwrap(t *testing.T) {
	sentinel := errors.New("underlying failure")
	err := &LLMError{Code: CodeTimeout, Provider: "anthropic", Err: sentinel}

	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false, want true (Unwrap should expose Err)")
	}

	var le *LLMError
	if !errors.As(fmt.Errorf("wrap: %w", err), &le) {
		t.Fatal("errors.As failed to find wrapped *LLMError")
	}
	if le.Code != CodeTimeout {
		t.Errorf("le.Code = %q, want %q", le.Code, CodeTimeout)
	}
}

// TestLLMErrorCode_MatchesFallbackReason locks the two enumerations
// (LLMErrorCode's five values and ports.FallbackReason's five values)
// together -- internal/app/intentclassifier maps one directly onto the
// other, one-to-one, never via string-matching a message; if either
// enumeration ever grows or drifts independently of the other this test
// catches it immediately rather than leaving a silently-unmapped code.
func TestLLMErrorCode_MatchesFallbackReason(t *testing.T) {
	pairs := []struct {
		code   LLMErrorCode
		reason string
	}{
		{CodeNoAPIKey, FallbackReasonNoAPIKey},
		{CodeTimeout, FallbackReasonTimeout},
		{CodeInvalidOutput, FallbackReasonInvalidOutput},
		{CodeAPIError, FallbackReasonAPIError},
		{CodeUnsupportedProvider, FallbackReasonUnsupportedProvider},
	}
	for _, p := range pairs {
		if string(p.code) != p.reason {
			t.Errorf("LLMErrorCode %q does not match FallbackReason %q", p.code, p.reason)
		}
	}
}
