// This file (verdict.go) implements Step 56's ("workflow HITL gate +
// circuit breaker", §25.9) own GitHub-facing deterministic keyword: a new
// EditPrefix, the workflow-generic analogue of internal/domain/plan.
// RevisePrefix ("revise:") for the SAME "request changes, folding free text
// in as an extra instruction" shape -- but deliberately a DIFFERENT literal
// than plan's own "revise:", so a GitHub comment can never be ambiguous
// between "this is a plan-mode revision" (plan.MatchRevise, decided via the
// existing plans table and dispatch, completely unchanged by this Step) and
// "this is a workflow-step HITL revise verdict" (MatchEdit below, decided
// via workflow_step_runs and this Step's own decide endpoint) when BOTH a
// plan and a workflow step could, in principle, be awaiting a decision on
// the same session at once.
//
// "edit:" was the literal floated during this chantier's own planning
// discussion (§25.9's prose names EditPrefix by identifier but leaves the
// exact string to the implementing Step); no other candidate is named
// anywhere in docs/TECHNICAL_PLAN.md or docs/IMPLEMENTATION_PLAN.md, so
// "edit:" is what this Step commits to.
//
// approve/reject reuse internal/domain/plan.MatchVerdict UNCHANGED --
// §25.9's own text asks for exactly one NEW keyword (EditPrefix), not a
// parallel approve/reject vocabulary: "approve"/"approved"/"lgtm" and
// "reject"/"rejected"/"no" are generic English words with no plan-specific
// meaning baked into their spelling, so reusing that same exported,
// already-tested list here (a caller-side decision, not something this
// package needs its own copy of) is the conservative choice -- one shared
// approve/reject vocabulary across plan mode and the workflow HITL gate,
// never two independently-maintained near-duplicates.

package workflow

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// EditPrefix is the deterministic, case-insensitive PREFIX a GitHub comment
// must start with (once trimmed) to be recognized as a workflow-step
// "revise" verdict while that step's own attempt is awaiting_decision --
// mirrors internal/domain/plan.RevisePrefix's own exact rationale one level
// up: a genuinely free-text PR comment might easily mention the word "edit"
// in passing ("let's edit the config before merging") without meaning to
// invoke this deterministic override, so MatchEdit below only ever matches
// a PREFIX anchored at the very start of the (trimmed) comment, never a
// substring/contains check anywhere in it -- the same "whole string" (plan.
// MatchVerdict) / "anchored prefix" (plan.MatchRevise, this) discipline
// this codebase already applies once, applied again here for a second,
// independent deterministic vocabulary.
const EditPrefix = "edit:"

// MatchEdit reports whether text (a GitHub comment body, or any other raw
// reply text a caller passes unmodified -- this function does its own trim/
// case-fold, exactly like plan.MatchRevise) starts with EditPrefix once
// trimmed and lower-cased. ok is true iff it does; feedback is everything
// AFTER the prefix, with its own leading/trailing whitespace trimmed. An
// empty feedback after the prefix (just "edit:", or "edit:   ", alone)
// still reports ok=true with feedback == "" -- deciding what to do with an
// empty feedback prompt is entirely the caller's own job, exactly like
// plan.MatchRevise's own identical contract (see plan.IsBlankFeedback for
// the shared "is this effectively empty" check a caller applies to the
// result).
//
// Matches EditPrefix (pure ASCII) rune-by-rune directly against the
// ORIGINAL, un-folded trimmed string, consuming exactly len(EditPrefix)
// RUNES (never bytes) and cutting at the resulting (always-valid) rune
// boundary in trimmed itself -- deliberately the SAME algorithm as
// plan.MatchRevise, for the SAME documented reason (see that function's own
// doc comment in full): a naive "lower-case a copy, then slice the
// ORIGINAL at len(prefix) BYTES" implementation is correct only when every
// rune in the matched prefix happens to case-fold to a rune of the
// IDENTICAL UTF-8 byte length -- which breaks for a real character (İ,
// LATIN CAPITAL LETTER I WITH DOT ABOVE, U+0130, is 2 bytes in UTF-8 but
// case-folds to plain ASCII "i", 1 byte) that EditPrefix itself actually
// contains (the "i" in "edit:") -- so a reply like "edİt: drop the retry"
// exercises the EXACT SAME failure mode plan.MatchRevise's own regression
// test (verdict_test.go) already documents for "revİse:". Never reintroduce
// that class of bug here: this function never lower-cases a copy and
// slices the original at a byte offset computed from it.
func MatchEdit(text string) (feedback string, ok bool) {
	trimmed := strings.TrimSpace(text)

	remaining := trimmed
	for _, want := range EditPrefix {
		r, size := utf8.DecodeRuneInString(remaining)
		if size == 0 || unicode.ToLower(r) != want {
			return "", false
		}
		remaining = remaining[size:]
	}
	return strings.TrimSpace(remaining), true
}
