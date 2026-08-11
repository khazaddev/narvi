// This file (deliberately NOT behind the "integration" build tag, mirrors
// reviewverdicttoolprompt_test.go's own identical precedent) proves
// renderEpistemicOutcomeToolPromptText/epistemicOutcomeToolURL
// (epistemicoutcometoolprompt.go) directly, in-process.
//
// Unlike renderUploadToolPromptText's own hostile-filename exfiltration
// test (reviewverdicttoolprompt_test.go's own
// TestRenderUploadToolPromptText_HostileFilenameCannotExfiltrateSecrets),
// this file carries no equivalent: turn.RenderEpistemicPreamble's own
// text (internal/domain/turn/epistemicpreamble.go) is entirely static,
// with ZERO interpolated/untrusted content of any kind (no filename, no
// PR title, no user-supplied string ever flows into it) -- there is no
// attacker-controlled substring this substitution could ever be tricked
// into expanding, so that class of test genuinely does not apply here.
package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// TestRenderEpistemicOutcomeToolPromptText mirrors
// TestRenderVerdictToolPromptText's own table shape exactly (Step 61,
// §20.2): the SAME mechanism, a third placeholder set, resolved against
// the SAME controlPlaneHTTPBase derivation reviewVerdictToolURL/
// epistemicOutcomeToolURL both share.
func TestRenderEpistemicOutcomeToolPromptText(t *testing.T) {
	t.Parallel()

	preambleText := turn.RenderEpistemicPreamble()

	tests := []struct {
		name            string
		text            string
		cfg             *sessionconfig.SessionConfig
		wantExact       string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:      "no placeholders present: byte-for-byte no-op regardless of cfg",
			text:      "an ordinary build turn's own prompt, nothing epistemic-shaped here",
			cfg:       &sessionconfig.SessionConfig{ControlPlaneWsUrl: "wss://cp.example.com/sessions/abc/ws?type=sandbox", SessionId: "abc", SandboxToken: "tok", Gen: 3},
			wantExact: "an ordinary build turn's own prompt, nothing epistemic-shaped here",
		},
		{
			name:      "nil cfg: no-op even with placeholders present",
			text:      preambleText,
			cfg:       nil,
			wantExact: preambleText,
		},
		{
			name: "epistemic-check-enabled build turn, production wss:// control plane: all three placeholders resolved",
			text: preambleText,
			cfg: &sessionconfig.SessionConfig{
				ControlPlaneWsUrl: "wss://cp.example.com/sessions/session-123/ws?type=sandbox",
				SessionId:         "session-123",
				SandboxToken:      "s3cr3t-token",
				Gen:               7,
			},
			wantContains: []string{
				"POST https://cp.example.com/sessions/session-123/turn/epistemic-outcome",
				"Authorization: Bearer s3cr3t-token",
				"X-Sandbox-Gen: 7",
			},
			wantNotContains: []string{
				turn.EpistemicOutcomeToolURLPlaceholder, turn.EpistemicOutcomeToolBearerPlaceholder, turn.EpistemicOutcomeToolGenPlaceholder,
			},
		},
		{
			name: "epistemic-check-enabled build turn, loopback ws:// control plane (dev/test): resolved via http",
			text: preambleText,
			cfg: &sessionconfig.SessionConfig{
				ControlPlaneWsUrl: "ws://127.0.0.1:8080/sessions/session-9/ws?type=sandbox",
				SessionId:         "session-9",
				SandboxToken:      "dev-token",
				Gen:               1,
			},
			wantContains: []string{
				"POST http://127.0.0.1:8080/sessions/session-9/turn/epistemic-outcome",
				"Authorization: Bearer dev-token",
				"X-Sandbox-Gen: 1",
			},
		},
		{
			name: "epistemic-check-enabled build turn, non-loopback ws:// control plane: refused, placeholders left unresolved",
			text: preambleText,
			cfg: &sessionconfig.SessionConfig{
				ControlPlaneWsUrl: "ws://cp.example.com/sessions/session-5/ws?type=sandbox",
				SessionId:         "session-5",
				SandboxToken:      "should-never-appear",
				Gen:               2,
			},
			wantExact: preambleText,
		},
		{
			name: "epistemic-check-enabled build turn, malformed control plane url: refused, placeholders left unresolved",
			text: preambleText,
			cfg: &sessionconfig.SessionConfig{
				ControlPlaneWsUrl: "://not a url",
				SessionId:         "session-1",
				SandboxToken:      "should-never-appear",
				Gen:               1,
			},
			wantExact: preambleText,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderEpistemicOutcomeToolPromptText(tc.text, tc.cfg)

			if tc.wantExact != "" && got != tc.wantExact {
				t.Fatalf("renderEpistemicOutcomeToolPromptText() = %q, want exactly %q", got, tc.wantExact)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("renderEpistemicOutcomeToolPromptText() = %q, want it to contain %q", got, want)
				}
			}
			for _, notWant := range tc.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("renderEpistemicOutcomeToolPromptText() = %q, want it to NOT contain %q", got, notWant)
				}
			}
		})
	}
}

// TestRenderEpistemicOutcomeToolPromptText_NeverLeaksTokenWhenNothingToSubstitute
// mirrors TestRenderVerdictToolPromptText_NeverLeaksTokenWhenNothingToSubstitute's
// own identical proof, for the epistemic-outcome-tool placeholder set --
// the byte-for-byte no-op case that matters most in practice, since the
// feature is off by default (§20.4).
func TestRenderEpistemicOutcomeToolPromptText_NeverLeaksTokenWhenNothingToSubstitute(t *testing.T) {
	t.Parallel()

	const liveToken = "super-secret-live-sandbox-token"
	got := renderEpistemicOutcomeToolPromptText("build this feature please", &sessionconfig.SessionConfig{
		ControlPlaneWsUrl: "wss://cp.example.com/sessions/abc/ws?type=sandbox",
		SessionId:         "abc",
		SandboxToken:      liveToken,
		Gen:               1,
	})

	if strings.Contains(got, liveToken) {
		t.Errorf("renderEpistemicOutcomeToolPromptText() = %q, want it to NEVER contain the live sandbox token for a prompt with no placeholders", got)
	}
	if got != "build this feature please" {
		t.Errorf("renderEpistemicOutcomeToolPromptText() = %q, want the ordinary build prompt returned byte-for-byte unchanged", got)
	}
}

// TestEpistemicOutcomeToolURL is table-driven over every scheme/host
// branch epistemicOutcomeToolURL can take -- mirrors TestReviewVerdictToolURL's
// own identical table shape, one path over.
func TestEpistemicOutcomeToolURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		controlPlaneWsURL string
		sessionID         string
		want              string
		wantErr           bool
	}{
		{
			name:              "wss -> https",
			controlPlaneWsURL: "wss://cp.example.com/sessions/abc/ws?type=sandbox",
			sessionID:         "abc",
			want:              "https://cp.example.com/sessions/abc/turn/epistemic-outcome",
		},
		{
			name:              "ws + loopback ip -> http",
			controlPlaneWsURL: "ws://127.0.0.1:9090/sessions/abc/ws?type=sandbox",
			sessionID:         "abc",
			want:              "http://127.0.0.1:9090/sessions/abc/turn/epistemic-outcome",
		},
		{
			name:              "ws + localhost -> http",
			controlPlaneWsURL: "ws://localhost:9090/sessions/abc/ws?type=sandbox",
			sessionID:         "abc",
			want:              "http://localhost:9090/sessions/abc/turn/epistemic-outcome",
		},
		{
			name:              "ws + non-loopback host -> error",
			controlPlaneWsURL: "ws://cp.example.com/sessions/abc/ws?type=sandbox",
			sessionID:         "abc",
			wantErr:           true,
		},
		{
			name:              "unrecognized scheme -> error",
			controlPlaneWsURL: "https://cp.example.com/sessions/abc/ws?type=sandbox",
			sessionID:         "abc",
			wantErr:           true,
		},
		{
			name:              "malformed url -> error",
			controlPlaneWsURL: "://not a url",
			sessionID:         "abc",
			wantErr:           true,
		},
		{
			name:              "sessionID is path-escaped",
			controlPlaneWsURL: "wss://cp.example.com/sessions/abc/ws?type=sandbox",
			sessionID:         "a/b c",
			want:              "https://cp.example.com/sessions/a%2Fb%20c/turn/epistemic-outcome",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := epistemicOutcomeToolURL(tc.controlPlaneWsURL, tc.sessionID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("epistemicOutcomeToolURL() = %q, nil error, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("epistemicOutcomeToolURL() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("epistemicOutcomeToolURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderEpistemicOutcomeAndVerdictPlaceholders_Independent mirrors
// TestRenderUploadAndVerdictPlaceholders_Independent's own identical
// proof: the THREE substitution passes main.go's HandlePrompt now runs in
// sequence (verdict, then upload, then epistemic-outcome) do not
// interfere with each other. A hypothetical future turn carrying both a
// review verdict block and an epistemic preamble is not a real shape
// today (a review turn never gets the builder epistemic preamble, §20's
// own scope), but the substitution mechanism itself must stay
// independent regardless.
func TestRenderEpistemicOutcomeAndVerdictPlaceholders_Independent(t *testing.T) {
	t.Parallel()

	text := "POST " + turn.EpistemicOutcomeToolURLPlaceholder + " Bearer " + turn.EpistemicOutcomeToolBearerPlaceholder + "\n" +
		"X-Sandbox-Gen: " + turn.EpistemicOutcomeToolGenPlaceholder

	cfg := &sessionconfig.SessionConfig{
		ControlPlaneWsUrl: "wss://cp.example.com/sessions/session-1/ws?type=sandbox",
		SessionId:         "session-1",
		SandboxToken:      "tok-1",
		Gen:               4,
	}

	got := renderVerdictToolPromptText(text, cfg)
	got = renderUploadToolPromptText(got, cfg)
	got = renderEpistemicOutcomeToolPromptText(got, cfg)

	if strings.Contains(got, turn.EpistemicOutcomeToolURLPlaceholder) {
		t.Fatalf("renderEpistemicOutcomeToolPromptText(...) = %q, want the epistemic-outcome placeholder resolved", got)
	}
	want := "POST https://cp.example.com/sessions/session-1/turn/epistemic-outcome Bearer tok-1\n" +
		"X-Sandbox-Gen: " + strconv.Itoa(4)
	if got != want {
		t.Errorf("got = %q, want %q", got, want)
	}
}
