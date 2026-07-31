// This file (reviewverdicttoolprompt.go) closes the other half of Step
// 47's ("server-side verdict", §8.2/§5.2/§21.2) own verdict-posting tool:
// internal/domain/review.RenderTurnPrompt (Step 46/47) renders a review
// turn's prompt with a FIXED, deterministic block instructing the agent
// how to call POST /sessions/{sessionID}/review/verdict -- but that
// package runs at TURN-CREATION time, in the control plane, before any
// sandbox exists (a brand-new review session) or before any respawn of an
// existing one (a NEW gen, a NEW rotated §5.2 token) -- so it can only
// ever emit PLACEHOLDER tokens (review.VerdictToolURLPlaceholder et al.,
// see that package's own doc comment) in place of this turn's real,
// live, CURRENT-gen URL/bearer/gen.
//
// This sandbox-agent process is the ONE place in the whole system where a
// specific, about-to-run turn's sessionID, SandboxToken, and Gen are all
// simultaneously and CURRENTLY in scope together -- it already holds all
// three (cfg.SessionConfig, read from its own NARVI_SESSION_CONFIG at
// boot) for the sandbox WS handshake and the scm-credentials/
// snapshot-mint calls it already makes. renderVerdictToolPromptText below
// substitutes the placeholders for those real, live values immediately
// before a "prompt" command's own Text is handed to OpenCode
// (commandHandler.HandlePrompt, main.go) -- never persisted, never
// logged.
//
// Deliberately unconditional, with no "is this a review session" flag
// anywhere on the wire (sandboxws.Prompt, §6.1, carries none, and this
// file adds none): a prompt with none of the three placeholders -- every
// non-review turn, since review.RenderTurnPrompt is never called to build
// one -- is returned byte-for-byte unchanged by strings.ReplaceAll's own
// documented no-op behavior when the substring it is asked to replace is
// absent. The placeholders' own presence already is the only signal this
// substitution needs.
package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/review"
)

// renderVerdictToolPromptText substitutes review.VerdictToolURLPlaceholder/
// VerdictToolBearerPlaceholder/VerdictToolGenPlaceholder in text with this
// sandbox's OWN, live, current-gen values, derived from cfg -- see this
// file's own top doc comment for the full rationale. cfg == nil (should be
// unreachable -- commandHandler.HandlePrompt only ever calls this when
// h.cfg.SessionConfig is already known non-nil, see run()'s own
// "commandHandler only ever constructed within that same cfg.SessionConfig
// != nil branch" precedent) returns text unchanged, matching this
// package's existing "no live session, nothing to do" discipline
// elsewhere (e.g. HandlePrompt/HandleStop's own h.adapter == nil guards).
//
// A malformed cfg.ControlPlaneWsUrl (reviewVerdictToolURL's own error
// path) is logged and otherwise treated as "nothing to substitute" --
// this is an existing, separate bug in this sandbox's own boot config were
// it ever to happen, not something this function should newly promote to
// turn-fatal: a review agent that cannot resolve the placeholders simply
// cannot call the tool this turn, exactly as if this substitution had
// never run at all (it never has, before this Step).
func renderVerdictToolPromptText(text string, cfg *sessionconfig.SessionConfig) string {
	if cfg == nil {
		return text
	}
	if !strings.Contains(text, review.VerdictToolURLPlaceholder) &&
		!strings.Contains(text, review.VerdictToolBearerPlaceholder) &&
		!strings.Contains(text, review.VerdictToolGenPlaceholder) {
		// The overwhelming common case (every non-review turn): nothing to
		// substitute, skip deriving a URL at all.
		return text
	}

	verdictURL, err := reviewVerdictToolURL(cfg.ControlPlaneWsUrl, cfg.SessionId)
	if err != nil {
		slog.Warn("sandbox-agent: derive review-verdict-tool URL failed; leaving prompt placeholders unresolved", "error", err)
		return text
	}

	text = strings.ReplaceAll(text, review.VerdictToolURLPlaceholder, verdictURL)
	text = strings.ReplaceAll(text, review.VerdictToolBearerPlaceholder, cfg.SandboxToken)
	text = strings.ReplaceAll(text, review.VerdictToolGenPlaceholder, strconv.Itoa(cfg.Gen))
	return text
}

// reviewVerdictToolURL derives the CP-HTTP POST /sessions/{sessionID}/
// review/verdict URL from controlPlaneWsUrl (SessionConfig.
// ControlPlaneWsUrl, a ws(s)://.../sessions/{id}/ws?type=sandbox URL,
// §6.1) -- mirrors internal/sandboxagent/credentials.NewCPClient's own
// identical wss->https/ws->http scheme-swap-plus-loopback-guard exactly
// (that package's own doc comment: "there is no separate REST base URL
// field in SESSION_CONFIG... this is a documented, reasoned derivation"),
// duplicated here rather than exported cross-package for such a small,
// one-off derivation -- the same "small, local, dependency-free helper"
// precedent internal/adapters/inbound/httpapi's own sessionRepoHosts
// establishes elsewhere in this codebase. The loopback guard matters just
// as much here as it does in NewCPClient: this URL, bearer token, and gen
// are about to be embedded verbatim in a review turn's own prompt text,
// so a plaintext ws:// control plane on anything but loopback would mean
// this sandbox token travels in the clear the moment the review agent
// actually calls it -- refused here for the identical reason NewCPClient
// refuses it.
func reviewVerdictToolURL(controlPlaneWsURL, sessionID string) (string, error) {
	parsed, err := url.Parse(controlPlaneWsURL)
	if err != nil {
		return "", fmt.Errorf("sandbox-agent: parse control plane ws url %q: %w", controlPlaneWsURL, err)
	}

	var httpScheme string
	switch parsed.Scheme {
	case "wss":
		httpScheme = "https"
	case "ws":
		if !isLoopbackHost(parsed.Host) {
			return "", fmt.Errorf("sandbox-agent: plaintext ws:// control plane url is only allowed for a loopback host, got %q", parsed.Host)
		}
		httpScheme = "http"
	default:
		return "", fmt.Errorf("sandbox-agent: control plane ws url %q has unrecognized scheme %q, want ws or wss", controlPlaneWsURL, parsed.Scheme)
	}

	return httpScheme + "://" + parsed.Host + "/sessions/" + url.PathEscape(sessionID) + "/review/verdict", nil
}

// isLoopbackHost reports whether hostport's host component (a bare
// hostname, or "host:port") is loopback -- "localhost", or an IP for which
// net.IP.IsLoopback() is true (127.0.0.0/8, ::1). Duplicates internal/
// sandboxagent/credentials.CPClient's own identical, unexported helper of
// the same name -- see reviewVerdictToolURL's own doc comment for why this
// is a deliberate, small duplication rather than a cross-package export.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
