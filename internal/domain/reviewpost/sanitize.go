package reviewpost

import "github.com/khazaddev/narvi/internal/domain/review"

// This file implements the review digest's own WRITE-PATH hardening
// (an adversarial-review fix, hardening the class PR #188 closed on
// the READ path). §5.2/review/sanitize.go's own doc comment establish the
// read-path half of this story: a review turn's prompt embeds entirely
// attacker-controlled PR content (diff/title/body) verbatim, and
// cmd/sandbox-agent's own unconditional, whole-prompt strings.ReplaceAll
// substitution passes mean a literal placeholder token
// ("{{REVIEW_VERDICT_TOOL_BEARER}}" and nine siblings, review.
// StripPlaceholderTokens' own doc comment) planted anywhere in that
// attacker-controlled text gets expanded into that turn's REAL, live
// sandbox bearer -- the credential for every sandbox-bearer endpoint,
// including scm-credentials, provider-credentials, verdict-posting, and
// uploads.
//
// # Why the WRITE path, not just the read path, needs this too
//
// Digest is different in kind from the diff/title/body review/sanitize.go
// already neutralizes: it is not attacker-controlled PR content read INTO
// a prompt, but MODEL-authored free prose written AFTER reading that
// attacker-controlled content -- Summary/AdequacyExplanation/StackRisks/
// UnverifiedLimits/ProposedBody/ContestedPoints, and each ArchDecision's
// own Decision/RejectedAlternative/ConventionConformance (digest.go's own
// doc comment enumerates exactly this list). A prompt-injected model can
// be steered into ECHOING an attacker's own planted placeholder token
// verbatim into any of these fields just as easily as the attacker
// planted it in the diff in the first place -- and unlike the diff/title/
// body, which review/sanitize.go neutralizes before they ever reach a
// prompt, these fields are persisted STRAIGHT to review_verdicts
// (internal/app/reviewverdict.Insert) with the only gate anywhere on that
// path being a non-blank check (validate.go's own
// ErrEmptyDigestSummary/ErrEmptyAdequacyExplanation/etc.) -- no token
// stripping, no length cap, before this Step.
//
// Nothing today re-reads a stored digest back into a LATER prompt (the one
// production read of a stored Digest, internal/adapters/inbound/github's
// own archrecapcontest.go, only ever HASHES it, never renders it into
// prompt text -- see ComputeDigestSectionIdentity's own doc comment,
// digestsectionidentity.go) -- so this fix closes no LIVE exploit path by
// itself today. It exists on its own merits regardless: a knowledge/
// projection mechanism that re-injects a stored digest into a LATER
// review's own prompt is designed and pending, and the moment it exists, a
// stored ArchDecision.Decision carrying a planted
// "{{REVIEW_VERDICT_TOOL_BEARER}}" retrieved into review #2's prompt would
// be expanded into THAT review's own live bearer -- the SAME CRITICAL
// class, but WORSE: a diff is ephemeral to the one review that fetched it;
// a poisoned digest, once stored, is retrieved into every future review
// that projection ever surfaces it to. Hardening the write path now means
// the stored byte can never carry a placeholder in the first place,
// whatever future read path eventually appears -- the read path's own
// egress-to-prompt guard (review/sanitize.go) stays exactly what it is
// today, an independent, defense-in-depth second layer, never the ONLY
// layer standing between a poisoned digest and a live secret.
//
// # Escaping choice: token-strip PLUS '<'/'>', matching sanitizeDescriptionField
//
// Every field this hardens is prose, not diff/source-code content a
// reviewer must read byte-for-byte faithfully -- review.
// sanitizeDescriptionField's own precedent (Title/Body: short,
// human-or-model-authored text, no legitimate reason to preserve a literal
// '<'/'>') applies here for the identical reason, so SanitizeDigest applies
// the SAME two-part treatment: StripPlaceholderTokens (the load-bearing
// half, above) PLUS escapeFindingDescription's own '<'/'>' -> '&lt;'/'&gt;'
// HTML-entity escaping (rendercomment.go) -- neutralizing the SAME
// delimiter-fence-forgery hazard sanitizeDescriptionField closes for
// Title/Body, in anticipation of a future read path that wraps a
// retrieved digest field in ITS OWN delimited prompt block (mirroring
// RenderTurnPrompt's own diffContentDelimiter/descriptionContentDelimiter
// fencing) -- a stored Decision containing a literal
// "</some_future_delimiter>" must not be able to close that future fence
// early, any more than a title/body containing the same text can close
// review/context.go's own fences today.
//
// escapeFindingDescription is reused DIRECTLY (rendercomment.go, same
// package) rather than a second, hand-rolled escaper: it is already this
// package's own single call site for exactly this '<'/'>' treatment,
// applied to every one of these SAME digest fields at RENDER time
// (rendercomment.go's own doc comment already lists Summary/
// AdequacyExplanation/StackRisks/UnverifiedLimits/ProposedBody/
// ContestedPoints/ArchDecision.* verbatim as its own scope).
//
// # No double-escaping -- verified, not assumed
//
// SanitizeDigest is called from exactly ONE place, internal/app/
// reviewverdict.Insert, on ITS OWN local copy of the digest being
// persisted -- never on the VerdictInput.Digest value httpapi.
// PostReviewVerdict ALSO passes to RenderVerdictComment. Both call sites
// (reviewverdict.go: RenderVerdictComment at line ~522, then
// appreviewverdict.Insert at line ~633) read the SAME original,
// unsanitized in-memory Digest, independently -- Digest is passed by VALUE
// into both (never a pointer), and Go strings are immutable, so nothing
// SanitizeDigest does inside Insert can ever be observed by
// RenderVerdictComment's own, already-completed rendering. The posted
// GitHub comment therefore always reflects escapeFindingDescription
// applied EXACTLY ONCE, to the original value -- regardless of whether
// this function also independently escapes a SEPARATE copy on its way
// into storage. TestRenderVerdictComment_UnaffectedBySanitizeDigest
// (sanitize_test.go) pins this independence directly: rendering the
// ORIGINAL digest produces single-escaped output whether or not
// SanitizeDigest has ALSO been called (on a different copy) in the same
// test.
func sanitizeDigestField(s string) string {
	return review.StripPlaceholderTokens(escapeFindingDescription(s))
}

// SanitizeDigest returns a copy of d with every model-authored free-text
// field (this file's own top doc comment enumerates the full list, and
// the "why" behind it) passed through sanitizeDigestField above --
// DescriptionAdequacy is deliberately left untouched: it is review's own
// closed, validated three-value enum (review.DescriptionAdequacy), never
// free text, so it carries no placeholder-token or delimiter-forgery
// hazard sanitizeDigestField exists to neutralize (mirrors
// rendercomment.go's own RenderVerdictComment, which renders
// digest.DescriptionAdequacy directly, with no escaping call, for the
// identical reason).
//
// The ONE sanctioned way this package's own write path (internal/app/
// reviewverdict.Insert, the sole intended caller) obtains a
// storage-safe Digest -- mirrors BuildVerdict/BuildFindings' own identical
// "one function, the one sanctioned transformation" idiom (validate.go).
// Pure per §11: no I/O, no time.Now(), no randomness -- a plain string/
// slice transformation, safe to call on every verdict unconditionally,
// light and deep alike (this hardening has nothing to do with review
// depth).
//
// ArchDecisions: nil/empty in produces nil out (mirrors BuildFindings' own
// identical "nil in, nil out" precedent, validate.go) -- a brand-new
// slice is allocated only when d.ArchDecisions is non-empty, so this
// function never mutates the caller's own backing array (which the
// caller, per this file's own top doc comment, still needs untouched for
// RenderVerdictComment's own, independent, already-run rendering).
func SanitizeDigest(d Digest) Digest {
	d.Summary = sanitizeDigestField(d.Summary)
	d.StackRisks = sanitizeDigestField(d.StackRisks)
	d.UnverifiedLimits = sanitizeDigestField(d.UnverifiedLimits)
	d.AdequacyExplanation = sanitizeDigestField(d.AdequacyExplanation)
	d.ProposedBody = sanitizeDigestField(d.ProposedBody)
	d.ContestedPoints = sanitizeDigestField(d.ContestedPoints)

	if len(d.ArchDecisions) > 0 {
		sanitized := make([]ArchDecision, len(d.ArchDecisions))
		for i, ad := range d.ArchDecisions {
			sanitized[i] = ArchDecision{
				Decision:              sanitizeDigestField(ad.Decision),
				RejectedAlternative:   sanitizeDigestField(ad.RejectedAlternative),
				ConventionConformance: sanitizeDigestField(ad.ConventionConformance),
			}
		}
		d.ArchDecisions = sanitized
	}

	return d
}
