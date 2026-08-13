package archrecap

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrEmptyReason is ValidateReason's own rejection -- see that function's
// own doc comment.
var ErrEmptyReason = errors.New("archrecap: reason must not be empty")

// Prefix is the deterministic, case-insensitive PREFIX a PR-thread comment
// must start with (once trimmed) to be recognized as an arch-recap-contest
// command (§26.5) -- mirrors internal/domain/falsepositive.Prefix's own
// identical reasoning for why this is a PREFIX match, never a whole-string
// or substring match: the text after the prefix is itself meaningful
// content (the maintainer's own reason), and a genuinely free-text comment
// might easily mention the words "arch recap" in passing without meaning
// to invoke the contest command at all -- Match below only ever matches a
// prefix anchored at the very start of the (trimmed) comment body, never a
// substring/contains check anywhere in it.
const Prefix = "arch recap wrong:"

// Match reports whether text (a PR comment's own raw body, exactly as
// received from the GitHub webhook payload -- this function does its own
// trim/case-fold, so callers pass it unmodified) starts with Prefix once
// trimmed and lower-cased. ok is true iff it does; reason is everything
// AFTER the prefix, with its own leading/trailing whitespace trimmed.
// Mirrors falsepositive.Match's own identical rune-by-rune matching
// exactly -- that function's own doc comment records the real Unicode
// byte-offset bug this shape was already hardened against: lower-casing a
// COPY to check the prefix, then slicing the ORIGINAL at a fixed BYTE
// offset, breaks the moment a matched rune's case-folded form has a
// different UTF-8 byte length than its original, e.g. İ U+0130 -> "i".
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
// "arch recap wrong:" with nothing explaining WHAT was wrong, or why,
// teaches nothing useful and would render as an empty, meaningless entry
// in the contestation-rate KPI's own audit trail. Mirrors
// falsepositive.ValidateReason's own identical "named error per rejected
// field" discipline, one field, one check.
func ValidateReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrEmptyReason
	}
	return nil
}
