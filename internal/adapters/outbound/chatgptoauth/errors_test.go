package chatgptoauth

import "testing"

func TestTokenError_IsTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want bool
	}{
		{"invalid_grant is terminal", "invalid_grant", true},
		{"refresh_token_reused is terminal", "refresh_token_reused", true},
		{"invalid_request is transient (default)", "invalid_request", false},
		{"server_error is transient (default)", "server_error", false},
		{"empty code (unparseable body) is transient (default)", "", false},
		{"unrecognized code is transient (default)", "something_new_openai_added", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := &TokenError{StatusCode: 400, Code: tc.code}
			if got := e.IsTerminal(); got != tc.want {
				t.Errorf("(&TokenError{Code: %q}).IsTerminal() = %v, want %v", tc.code, got, tc.want)
			}
			if e.Error() == "" {
				t.Error("Error() = empty, want a non-empty message")
			}
		})
	}
}
