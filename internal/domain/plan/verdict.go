package plan

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ApproveKeywords/RejectKeywords are Step 38's ("plan mode, cross-channel",
// §8.1/§13.3) own deterministic, case-insensitive, trimmed keyword set for
// a Linear text-based plan verdict (payload.AgentActivity.Content.Body,
// internal/adapters/inbound/linear/webhook.go's handlePrompted). Declared
// HERE, in this pure domain package (no I/O, §11), so both the
// plan-approval-request notification TEXT (internal/app/sessionactor/
// outboxenqueue.go, telling the user what to reply) and the PARSING logic
// itself (handlePrompted, deciding what a reply actually means) reference
// this SAME single list -- the instructions shown to the user can never
// drift out of sync with what is actually accepted, since there is only
// ever one place either could be edited.
//
// Keeping this list short and unambiguous is deliberate: a genuinely
// free-text reply that happens to contain one of these words as a
// substring of a longer sentence ("I don't think we should reject this
// early, let's approve it once X is fixed") is exactly the kind of
// ambiguity a DETERMINISTIC keyword match (§8.1's own explicit
// requirement, IMPLEMENTATION_PLAN.md row 38) must refuse to guess at --
// MatchVerdict below only ever matches the WHOLE (trimmed, case-folded)
// reply against one of these exact words, never a substring/contains
// check.
var (
	// ApproveKeywords is the approve twin of RejectKeywords below.
	ApproveKeywords = []string{"approve", "approved", "lgtm"}
	// RejectKeywords is the reject twin of ApproveKeywords above.
	RejectKeywords = []string{"reject", "rejected", "no"}
)

// MatchVerdict reports whether text (Linear's own raw reply body, exactly
// as received -- this function does its own trim/case-fold, so callers
// pass it unmodified) is a deterministic approve or reject keyword,
// matching the WHOLE trimmed, lower-cased string against ApproveKeywords/
// RejectKeywords -- never a substring/contains match (see this file's own
// top doc comment for why). verdict is "approve" or "reject" (matching
// httpapi.PlanVerdict's own exact string values, so a caller across the
// package boundary can convert with a plain string cast); ok is false for
// any other text at all, INCLUDING empty text -- the caller's own
// fallback, in every ok-false case, is to treat the reply as an ordinary
// turn prompt, completely unchanged from before this Step (handlePrompted's
// own pre-existing behavior).
func MatchVerdict(text string) (verdict string, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return "", false
	}
	for _, kw := range ApproveKeywords {
		if normalized == kw {
			return "approve", true
		}
	}
	for _, kw := range RejectKeywords {
		if normalized == kw {
			return "reject", true
		}
	}
	return "", false
}

// RevisePrefix is the deterministic, case-insensitive PREFIX a chat reply
// must start with (once trimmed) to be recognized as a "request changes"
// reply while a plan is awaiting_approval -- this batch's own follow-up fix
// (§8.1, closing the "reply matching no verdict keyword dispatches an
// ordinary build turn anyway" hole discovered during design review of Steps
// 37/38). Chosen alongside ApproveKeywords/RejectKeywords for the exact
// same reason: a genuinely free-text reply might easily mention the word
// "revise" in passing ("let's revise the approach before shipping") without
// meaning to invoke this deterministic override, so MatchRevise below only
// ever matches a PREFIX anchored at the very start of the (trimmed) reply,
// never a substring/contains check anywhere in it -- mirroring
// MatchVerdict's own "whole string, never a substring" discipline one level
// down (a prefix here, instead of whole-string equality, only because the
// text AFTER the prefix is itself meaningful content -- the feedback --
// unlike a verdict reply, which carries no payload beyond the keyword
// itself).
//
// This is deliberately the SIMPLEST possible deterministic override, not a
// natural-language "did they mean to request changes" classifier: a future
// Step is expected to replace prefix-detection with a real amend-vs-answer
// LLM classifier for the common case, with RevisePrefix remaining available
// afterward as a deterministic fallback a user can always reach for.
const RevisePrefix = "revise:"

// MatchRevise reports whether text (the reply's own raw body, exactly as
// received -- like MatchVerdict, this function does its own trim/case-fold)
// starts with RevisePrefix once trimmed and lower-cased. ok is true iff it
// does; feedback is everything AFTER the prefix, with its own leading/
// trailing whitespace trimmed (so "  Revise:   drop the retry  " yields
// feedback "drop the retry"). An empty feedback after the prefix (just
// "revise:", or "revise:   ", alone) still reports ok=true with feedback ==
// "" -- exactly like MatchVerdict, this function only ever reports whether
// its own deterministic pattern matched; deciding what to do with an empty
// feedback prompt is entirely the caller's own job.
//
// Bug-fix note (MEDIUM audit finding, Unicode byte-offset bug): this used
// to lower-case a COPY of trimmed (via strings.ToLower) to check the
// prefix, then slice the ORIGINAL, un-folded trimmed string at
// len(RevisePrefix) BYTES -- correct only when every rune in the matched
// prefix happens to case-fold to a rune of the IDENTICAL UTF-8 byte
// length. That assumption breaks for a real character: İ (LATIN CAPITAL
// LETTER I WITH DOT ABOVE, U+0130) is 2 bytes in UTF-8, but
// strings.ToLower's simple case mapping folds it to plain ASCII "i" (1
// byte) -- so "revİse: drop the retry" used to lower-case to exactly
// "revise: drop the retry" (matching the len(RevisePrefix) == 7 byte
// prefix check), but then slicing the ORIGINAL 8-byte-prefix string at 7
// bytes landed one byte short, leaking the trailing ":" into feedback
// (": drop the retry" -- see verdict_test.go's own regression case).
// Fixed by matching RevisePrefix (pure ASCII) rune-by-rune directly
// against the ORIGINAL, un-folded trimmed string, consuming exactly
// len(RevisePrefix) RUNES (never bytes) and cutting at the resulting
// (always-valid) rune boundary in trimmed itself -- so the returned
// feedback is always the ORIGINAL bytes after the prefix, byte-for-byte,
// regardless of any case-fold byte-length change within the prefix.
func MatchRevise(text string) (feedback string, ok bool) {
	trimmed := strings.TrimSpace(text)

	remaining := trimmed
	for _, want := range RevisePrefix {
		r, size := utf8.DecodeRuneInString(remaining)
		if size == 0 || unicode.ToLower(r) != want {
			return "", false
		}
		remaining = remaining[size:]
	}
	return strings.TrimSpace(remaining), true
}
