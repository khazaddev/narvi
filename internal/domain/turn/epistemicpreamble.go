package turn

// This file implements Step 61's ("domain/turn: builder epistemic
// pre-action check", §20) own devil's-advocate preamble and the config
// threading that decides whether a given build turn gets one.
//
// Pure per §11 (no I/O, no time.Now(), no randomness) -- this file imports
// nothing at all, matching internal/domain/review and internal/domain/
// upload's own "zero external imports" convention for a rendered-prompt-
// fragment package (review/doc.go, upload/doc.go).
//
// SHAPE PRECEDENT VERIFIED AGAINST REAL CODE (recorded here because §20.4's
// own wording -- "mirrors plan_mode's own nullable-column threading" --
// does not hold literally): turns.plan_mode
// (migrations/000018_session_repos.up.sql) is a plain NOT NULL boolean,
// always caller-supplied per turn, with no platform.Config-level default
// and no session-level override of its own anywhere in this codebase
// (grepped directly: platform.Config has zero fields mentioning "plan" in
// any case). The REAL nullable-column, session-overrides-global mechanism
// already established here is sessions.build_model_id/build_effort
// (migrations/000034_plan_mode.up.sql, migrations/000063_turn_session_
// effort.up.sql) -- introduced in the SAME "plan mode" body of work §20.4
// points to, just riding on the model/effort columns rather than the
// plan_mode column itself. ResolveEpistemicCheckEnabled below mirrors
// THAT real mechanism (a session-level nullable override, consulted at
// turn-creation time, session-wins-when-set), extended with the one tier
// build_model_id/build_effort don't have: those two fall back to the
// model catalog's own default when unset, whereas §20.4 explicitly calls
// for a platform.Config global default here, which this package's own
// caller (httpapi's createTurnLocked) supplies.
//
// ResolveEpistemicCheckEnabled/ShouldInjectEpistemicPreamble are
// deliberately two separate, narrow pure functions -- mirrors internal/
// domain/review's own PremiseFloor/ComputeShippable split (one function
// per decision, composed by the caller) rather than one function doing
// both the global/session resolution AND the plan-mode exclusion: a
// future caller that needs "is the check enabled for this session at
// all" (e.g. an analytics view, independent of any one turn's own
// plan_mode) can call the first alone.

// ResolveEpistemicCheckEnabled applies §20.4's precedence -- "session
// override wins when set, global default otherwise" -- given the
// platform-wide default (platform.Config, read and passed in by the
// caller; this package imports nothing, see this file's own top doc
// comment) and the session's own persisted override, if any (nil means
// this session has never set one).
func ResolveEpistemicCheckEnabled(platformDefault bool, sessionOverride *bool) bool {
	if sessionOverride != nil {
		return *sessionOverride
	}
	return platformDefault
}

// ShouldInjectEpistemicPreamble is the ONE place §20.3's plan-mode
// exclusion is decided -- "a turn running under plan_mode=true never gets
// the devil's-advocate preamble" -- made structural and obvious (the
// Step's own instruction) by living in a single, small, exhaustively
// tested pure function every caller MUST route through, rather than an
// inline `if !planMode` condition a future edit could drop or
// mis-order relative to the enabled check. planMode=true short-circuits
// to false regardless of checkEnabled -- there is no way to construct a
// true result for a planning turn through this function.
func ShouldInjectEpistemicPreamble(checkEnabled bool, planMode bool) bool {
	if planMode {
		return false
	}
	return checkEnabled
}

// EpistemicOutcomeToolURLPlaceholder, EpistemicOutcomeToolBearerPlaceholder,
// and EpistemicOutcomeToolGenPlaceholder are fixed tokens
// RenderEpistemicPreamble carries in place of a turn's real epistemic-
// outcome-reporting endpoint URL, sandbox bearer token, and X-Sandbox-Gen
// value -- mirrors internal/domain/review's own VerdictToolURLPlaceholder/
// VerdictToolBearerPlaceholder/VerdictToolGenPlaceholder (review/context.go)
// exactly, for the identical reason stated there: this package runs at
// turn-creation time, in the control plane, before any sandbox/gen for
// THIS dispatch necessarily exists yet (a brand-new session, or before any
// respawn -- a NEW gen, a NEW rotated §5.2 token -- of an existing one), so
// it can never embed a live secret. Resolved to real, current-gen values
// exactly once, inside sandbox-agent itself, immediately before a turn's
// own prompt text is handed to OpenCode -- see cmd/sandbox-agent's own
// epistemicoutcometoolprompt.go, which mirrors reviewverdicttoolprompt.go's
// own substitution mechanism rather than inventing a second one.
const (
	EpistemicOutcomeToolURLPlaceholder    = "{{EPISTEMIC_OUTCOME_TOOL_URL}}"
	EpistemicOutcomeToolBearerPlaceholder = "{{EPISTEMIC_OUTCOME_TOOL_BEARER}}"
	EpistemicOutcomeToolGenPlaceholder    = "{{EPISTEMIC_OUTCOME_TOOL_GEN}}"
)

// epistemicPreamble is RenderEpistemicPreamble's own fixed, deterministic
// text (§20.1) -- trusted, first-party instructional text (unlike a
// review turn's diff/stack blocks, review/context.go), so unlike those it
// is never delimiter-wrapped as untrusted content: it is part of what
// THIS SYSTEM tells the agent to do, exactly like a turn's own base
// prompt text.
//
// Three things this text must do, all required by §20 and pinned by this
// package's own characterization test (epistemicpreamble_test.go):
//
//  1. Ask the agent to consider, IN ORDER, the three §20.1 questions
//     (unverified assumption; contradicts something already observed
//     this session; otherwise worth a second look).
//  2. State the two-tier taxonomy (MINOR/STRONG) AND the deliberate
//     proceed-bias explicitly, in the text itself -- §20.1: "This bias is
//     stated explicitly in the preamble text itself, not left to the
//     model's own judgment of 'how cautious to be.'" The taxonomy names,
//     their own stopping/proceeding behavior, AND the instruction to stay
//     silent below MINOR are all spelled out here specifically so no
//     future edit to this file can accidentally drop the bias while
//     keeping the taxonomy's own names intact.
//  3. Instruct the agent how to report the structured EpistemicOutcome
//     (§20.2) via this turn's own reporting endpoint -- never optional,
//     never satisfied by the taxonomy classification appearing only in
//     the agent's own natural-language reply (§20.2's own "never
//     prompt-only" requirement; this codebase's standing invariant that a
//     structured signal is a typed field on a payload, never a marker
//     scraped from markdown, §26.4/§29).
const epistemicPreamble = "" +
	"Before you take any substantial action on this turn (editing files, running commands, opening a pull request, or similar), pause and think it through the way a deliberately skeptical second reviewer would, in this order:\n" +
	"1. Does anything about the action you are about to take rest on an assumption you have not actually verified -- about the codebase, the task, or your own prior steps this session?\n" +
	"2. Does it contradict anything you have already observed in this session?\n" +
	"3. Is there anything else about it that is genuinely worth a second look before you proceed?\n\n" +
	"Classify what you find using exactly these two tiers, as defined here -- not by your own sense of how cautious to be:\n" +
	"- MINOR: worth a heads-up, not worth stopping for. Proceed with the action, and mention what you noticed in your reply to the user.\n" +
	"- STRONG: worth stopping for. Do NOT take the action. Instead, explain the concern in your reply and wait for the user's response.\n\n" +
	"This check is DELIBERATELY BIASED TOWARD PROCEEDING: reserve STRONG for cases that genuinely warrant stopping, use MINOR for anything else worth mentioning, and if nothing rises to either tier, say NOTHING about this check in your reply -- do not manufacture a concern to report, and do not editorialize about how carefully you checked. Flagging routine, unremarkable work as a concern trains users to ignore this check, which defeats its entire purpose.\n\n" +
	"Whichever tier applies -- including when nothing rises to either one -- you MUST report it exactly once for this turn by calling this system's own endpoint below, a single authenticated HTTP request separate from your reply to the user:\n\n" +
	"POST " + EpistemicOutcomeToolURLPlaceholder + "\n" +
	"Authorization: Bearer " + EpistemicOutcomeToolBearerPlaceholder + "\n" +
	"X-Sandbox-Gen: " + EpistemicOutcomeToolGenPlaceholder + "\n" +
	"Content-Type: application/json\n\n" +
	"{\n" +
	"  \"outcome\": \"none\" | \"minor\" | \"strong\"\n" +
	"}\n\n" +
	"Use \"none\" when nothing rose to either tier. Do not skip this call regardless of outcome -- your reply's own wording is advisory only and is never re-read as the outcome of record; this call is.\n\n"

// MaybeInjectEpistemicPreamble is F6's own shared gate (adversarial
// review, Step 61): composes ResolveEpistemicCheckEnabled,
// ShouldInjectEpistemicPreamble, and RenderEpistemicPreamble into the ONE
// three-line sequence every raw turn-insert call site now routes through,
// rather than each duplicating it inline -- "duplication is exactly how
// the fifth site gets forgotten" (F6's own words). Mirrors createTurnLocked's
// own original inline sequence (httpapi/turn.go) byte-for-byte: resolve
// §20.4's precedence (session override wins when set, platformDefault
// otherwise), exclude any planMode==true turn per §20.3 regardless of how
// checkEnabled resolved, and -- only when injecting -- PRECEDE the
// preamble onto prompt (prepended, never appended, exactly like §20.1
// requires).
//
// Every caller of this function (httpapi's createTurnLocked/
// CreateSessionOnTx/DecidePlanOnTx, workflowengine's dispatchNextAttempt)
// is itself responsible for deciding what to pass as platformDefault --
// the real, operator-configured platform.Config.EpistemicCheckDefault for
// an ordinary build turn, or a hardcoded false for a turn this function's
// own caller knows is NOT a build turn (a review-session turn, F7) --
// exactly the same "every call site compile-time-decides" discipline
// epistemicCheckDefault already has as a required (never defaulted)
// parameter throughout this codebase.
func MaybeInjectEpistemicPreamble(platformDefault bool, sessionOverride *bool, planMode bool, prompt string) string {
	enabled := ResolveEpistemicCheckEnabled(platformDefault, sessionOverride)
	if ShouldInjectEpistemicPreamble(enabled, planMode) {
		return RenderEpistemicPreamble() + prompt
	}
	return prompt
}

// RenderEpistemicPreamble returns §20.1's own fixed devil's-advocate
// preamble text, to be PRECEDED -- prepended, never appended -- onto a
// build turn's own fully-assembled prompt text (§20.1: "the turn prompt
// is preceded by a short devil's-advocate preamble"), exactly when
// ShouldInjectEpistemicPreamble reports true for that turn. A function
// (rather than a bare exported const) both to mirror this package's
// sibling review/upload packages' own "Render..." naming for a rendered
// prompt fragment, and to leave room for this text to grow a real
// parameter later without a breaking signature change -- today it takes
// none, since (unlike review's own per-PR diff/stack context) there is no
// per-turn data to interpolate: the ONLY turn-specific values this text
// carries (the reporting endpoint's URL/bearer/gen) are the fixed
// placeholder tokens above, resolved sandbox-side, never rendered here.
func RenderEpistemicPreamble() string {
	return epistemicPreamble
}
