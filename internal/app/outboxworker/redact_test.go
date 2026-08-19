package outboxworker

import (
	"strings"
	"testing"
)

// TestRedactURLCredentials mirrors internal/adapters/outbound/modal's own
// TestRedactURLCredentials exactly (redact.go's own doc comment explains
// why this package duplicates, rather than imports, that helper) --
// covering both the case a well-formed absolute URL's own userinfo
// prefix is redacted, and the case a missing scheme (the shape a raw
// transport-error string is NOT guaranteed to avoid) still gets caught.
func TestRedactURLCredentials(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		wantRedacted  string
		wantNoRawPass string
	}{
		{
			name:          "absolute URL with credentials",
			in:            "http://user:hunter2@hooks.example.internal/services/T000/B000/XXXX",
			wantRedacted:  "http://user:xxxxx@hooks.example.internal/services/T000/B000/XXXX",
			wantNoRawPass: "hunter2",
		},
		{
			name:          "missing scheme with credentials",
			in:            "user:hunter2@hooks.example.internal",
			wantRedacted:  "user:xxxxx@hooks.example.internal",
			wantNoRawPass: "hunter2",
		},
		{
			name:          "plain error string with no URL or credentials at all",
			in:            "slackapi: chat.postMessage failed: channel_not_found",
			wantRedacted:  "slackapi: chat.postMessage failed: channel_not_found",
			wantNoRawPass: "hunter2",
		},
		{
			name:          "url.Error-shaped transport failure, no credentials in the URL",
			in:            `Post "https://slack.com/api/chat.postMessage": dial tcp: lookup slack.com: no such host`,
			wantRedacted:  `Post "https://slack.com/api/chat.postMessage": dial tcp: lookup slack.com: no such host`,
			wantNoRawPass: "hunter2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactURLCredentials(tt.in)
			if got != tt.wantRedacted {
				t.Errorf("redactURLCredentials(%q) = %q, want %q", tt.in, got, tt.wantRedacted)
			}
			if strings.Contains(got, tt.wantNoRawPass) {
				t.Errorf("redactURLCredentials(%q) = %q, must not contain the raw password %q", tt.in, got, tt.wantNoRawPass)
			}
		})
	}
}
