// This file (reviewcostbudgetprompt.go) closes §26.7/§26.9's own cost-
// budget mechanism (§26.5): internal/domain/review's own
// subAgentOrchestrationInstructions (context.go) renders a review turn's
// prompt with a FIXED placeholder token
// (review.ReviewCostBudgetToolURLPlaceholder) in place of the real, live
// GET URL for this sandbox's own loopback review-cost-budget server
// (reviewcostbudgetserver.go) -- that package runs at TURN-CREATION time,
// in the control plane, before any sandbox even exists, let alone one that
// has already bound an ephemeral port for this server, so it can only ever
// emit that placeholder (see its own doc comment, context.go, for the full
// "why", mirroring review.VerdictToolURLPlaceholder's identical reasoning).
//
// This sandbox-agent process is the ONE place in the whole system where
// this specific sandbox's own, already-bound loopback port is known --
// resolved once, at startup (run(), via budgetServer.URL()), stored on
// commandHandler.reviewCostBudgetURL, and substituted here immediately
// before a "prompt" command's own Text is handed to OpenCode
// (commandHandler.HandlePrompt, main.go) -- the SAME point in the SAME
// function reviewverdicttoolprompt.go's renderVerdictToolPromptText/
// renderUploadToolPromptText/epistemicoutcometoolprompt.go's
// renderEpistemicOutcomeToolPromptText already substitute their own
// placeholders at.
//
// Deliberately simpler than renderVerdictToolPromptText: no bearer, no
// gen, and no per-turn/per-gen derivation at all -- this sandbox's own
// loopback port is fixed for this ENTIRE sandbox-agent process's lifetime
// (chosen once, at boot, never rotated mid-session the way a sandbox
// bearer token is across respawns), so budgetURL is a plain, already-
// resolved string, not something this function derives itself from a
// SessionConfig the way reviewVerdictToolURL derives its own URL from
// ControlPlaneWsUrl.
package main

import (
	"strings"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// renderReviewCostBudgetToolPromptText substitutes
// review.ReviewCostBudgetToolURLPlaceholder in text with budgetURL, this
// sandbox's own real, already-bound loopback review-cost-budget URL -- see
// this file's own top doc comment for the full rationale. budgetURL == ""
// (no live session ever started this server, run()'s own
// cfg.SessionConfig != nil gating) returns text unchanged, matching this
// package's existing "no live session, nothing to do" discipline
// (renderVerdictToolPromptText's own identical nil-cfg precedent,
// reviewverdicttoolprompt.go).
//
// Reads review.ReviewCostBudgetToolURLPlaceholder directly (the SAME
// constant subAgentOrchestrationInstructions renders, internal/domain/
// review/context.go) rather than a hand-typed local copy, so this
// function's own substitution can never silently desynchronize from the
// real placeholder text a review turn's prompt actually carries.
func renderReviewCostBudgetToolPromptText(text, budgetURL string) string {
	if budgetURL == "" {
		return text
	}
	return strings.ReplaceAll(text, review.ReviewCostBudgetToolURLPlaceholder, budgetURL)
}
