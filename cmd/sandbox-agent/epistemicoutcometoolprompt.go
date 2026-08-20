// This file (epistemicoutcometoolprompt.go) closes the other half of
// §20.2's ("domain/turn: builder epistemic pre-action check") own
// structured-signal-reporting tool: internal/domain/turn.
// RenderEpistemicPreamble renders a build turn's own devil's-advocate
// preamble with a FIXED, deterministic block instructing the agent how to
// call POST /sessions/{sessionID}/turn/epistemic-outcome -- but that
// package runs at TURN-CREATION time, in the control plane, before any
// sandbox exists (a brand-new session) or before any respawn of an
// existing one (a NEW gen, a NEW rotated §5.2 token) -- so it can only
// ever emit PLACEHOLDER tokens (turn.EpistemicOutcomeToolURLPlaceholder et
// al., see that package's own doc comment) in place of this turn's real,
// live, CURRENT-gen URL/bearer/gen.
//
// Mirrors renderVerdictToolPromptText's own mechanism EXACTLY (see
// reviewverdicttoolprompt.go's own top doc comment for the full
// rationale) -- a THIRD tool sharing the SAME substitution scheme, never
// a second one invented for it: this sandbox-agent process is the ONE
// place in the whole system where a specific, about-to-run turn's
// sessionID, SandboxToken, and Gen are all simultaneously and CURRENTLY
// in scope together (cfg.SessionConfig, read from its own
// NARVI_SESSION_CONFIG at boot).
package main

import (
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// renderEpistemicOutcomeToolPromptText substitutes turn.
// EpistemicOutcomeToolURLPlaceholder/EpistemicOutcomeToolBearerPlaceholder/
// EpistemicOutcomeToolGenPlaceholder in text with this sandbox's OWN,
// live, current-gen values, derived from cfg -- mirrors
// renderVerdictToolPromptText's own structure and error handling exactly
// (nil cfg / malformed ControlPlaneWsUrl both degrade to "leave
// placeholders unresolved", never turn-fatal; see that function's own doc
// comment for the full reasoning, which applies here without
// modification).
func renderEpistemicOutcomeToolPromptText(text string, cfg *sessionconfig.SessionConfig) string {
	if cfg == nil {
		return text
	}
	if !strings.Contains(text, turn.EpistemicOutcomeToolURLPlaceholder) &&
		!strings.Contains(text, turn.EpistemicOutcomeToolBearerPlaceholder) &&
		!strings.Contains(text, turn.EpistemicOutcomeToolGenPlaceholder) {
		// The overwhelming common case (feature off by default, §20.4, or
		// a plan-mode turn, §20.3): nothing to substitute, skip deriving a
		// URL at all.
		return text
	}

	toolURL, err := epistemicOutcomeToolURL(cfg.ControlPlaneWsUrl, cfg.SessionId)
	if err != nil {
		slog.Warn("sandbox-agent: derive epistemic-outcome-tool URL failed; leaving prompt placeholders unresolved", "error", err)
		return text
	}

	text = strings.ReplaceAll(text, turn.EpistemicOutcomeToolURLPlaceholder, toolURL)
	text = strings.ReplaceAll(text, turn.EpistemicOutcomeToolBearerPlaceholder, cfg.SandboxToken)
	text = strings.ReplaceAll(text, turn.EpistemicOutcomeToolGenPlaceholder, strconv.Itoa(cfg.Gen))
	return text
}

// epistemicOutcomeToolURL derives the CP-HTTP POST /sessions/{sessionID}/
// turn/epistemic-outcome URL by appending its own fixed path onto
// controlPlaneHTTPBase's own scheme://host derivation (reviewverdicttoolprompt.go)
// -- mirrors reviewVerdictToolURL's own identical shape, one path over.
func epistemicOutcomeToolURL(controlPlaneWsURL, sessionID string) (string, error) {
	base, err := controlPlaneHTTPBase(controlPlaneWsURL)
	if err != nil {
		return "", err
	}
	return base + "/sessions/" + url.PathEscape(sessionID) + "/turn/epistemic-outcome", nil
}
