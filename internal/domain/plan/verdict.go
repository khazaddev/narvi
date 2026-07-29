package plan

import "strings"

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
