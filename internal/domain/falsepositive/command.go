package falsepositive

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrEmptyReason is ValidateReason's own rejection -- see that function's
// own doc comment.
var ErrEmptyReason = errors.New("falsepositive: reason must not be empty")

// Prefix is the deterministic, case-insensitive PREFIX a PR-thread comment
// must start with (once trimmed) to be recognized as a false-positive-
// pattern capture command (§22.2). Mirrors internal/domain/plan.
// RevisePrefix's own identical reasoning for why this is a PREFIX match,
// never a whole-string or substring match: the text after the prefix is
// itself meaningful content (the maintainer's own reason), and a
// genuinely free-text comment might easily mention the words "false
// positive" in passing ("I don't think this is a false positive, but...")
// without meaning to invoke the capture command at all -- Match below only
// ever matches a prefix anchored at the very start of the (trimmed)
// comment body, never a substring/contains check anywhere in it.
const Prefix = "false positive:"

// Match reports whether text (a PR comment's own raw body, exactly as
// received from the GitHub webhook payload -- this function does its own
// trim/case-fold, so callers pass it unmodified) starts with Prefix once
// trimmed and lower-cased. ok is true iff it does; reason is everything
// AFTER the prefix, with its own leading/trailing whitespace trimmed (so
// "  False Positive:   this is intentional  " yields reason "this is
// intentional"). An empty reason after the prefix (just "false positive:",
// or "false positive:   ", alone) still reports ok=true with reason == ""
// -- like plan.MatchRevise, this function only ever reports whether its
// own deterministic pattern matched; ValidateReason below is the caller's
// own separate check for whether an empty reason should actually be
// captured.
//
// Implementation mirrors plan.MatchRevise's own rune-by-rune matching
// exactly (that function's own doc comment records the real Unicode
// byte-offset bug this shape was already hardened against: lower-casing a
// COPY to check the prefix, then slicing the ORIGINAL at a fixed BYTE
// offset, breaks the moment a matched rune's case-folded form has a
// different UTF-8 byte length than its original, e.g. İ U+0130 -> "i").
// Applied here from this function's first version, not discovered later.
func Match(text string) (reason string, ok bool) {
	trimmed := strings.TrimSpace(text)

	remaining := trimmed
	for _, want := range Prefix {
		r, size := utf8.DecodeRuneInString(remaining)
		if size == 0 || unicode.ToLower(r) != want {
			return "", false
		}
		remaining = remaining[size:]
	}
	return strings.TrimSpace(remaining), true
}

// ValidateReason rejects an empty (post-Match, post-trim) reason -- a bare
// "false positive:" with nothing explaining WHAT is a false positive, or
// why, teaches nothing useful and would render as an empty, meaningless
// bullet in every future review's own advisory block (RenderAdvisoryBlock
// below). Mirrors reviewpost.ValidateFindingInput's own "named error per
// rejected field" discipline, one field, one check.
func ValidateReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrEmptyReason
	}
	return nil
}
