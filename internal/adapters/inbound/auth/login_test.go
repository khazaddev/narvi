package auth

import "testing"

// TestIsSafeRedirectNext is exhaustive over the shapes that matter: a
// genuine same-origin absolute path is accepted; anything that could be
// (mis)interpreted by a browser as pointing off-origin (empty, no leading
// slash, scheme-relative "//...", backslash-prefixed "/\\...", a full
// "https://..." URL) is rejected.
func TestIsSafeRedirectNext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		next string
		want bool
	}{
		{"plain absolute path", "/auth/identity-link/some-nonce", true},
		{"root path", "/", true},
		{"path with query string", "/foo?bar=baz", true},
		{"empty", "", false},
		{"relative, no leading slash", "foo/bar", false},
		{"scheme-relative double slash", "//evil.example.com", false},
		{"scheme-relative double slash with path", "//evil.example.com/path", false},
		{"backslash variant", "/\\evil.example.com", false},
		{"full https url", "https://evil.example.com", false},
		{"full http url", "http://evil.example.com/", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isSafeRedirectNext(tt.next); got != tt.want {
				t.Errorf("isSafeRedirectNext(%q) = %v, want %v", tt.next, got, tt.want)
			}
		})
	}
}
