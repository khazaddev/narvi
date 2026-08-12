package findingposition

import (
	"context"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/platform"
)

// ResolveAll is httpapi.PostReviewVerdict's own one call site for §22.1.1:
// given findings (a verdict's own already-built []reviewpost.Finding,
// reviewpost.BuildFindings) and diff (the SAME diff the reviewing agent's
// own turn was anchored to, fetched fresh at posting time -- internal/app/
// reviewcontext.FetchDiffAt), it returns a NEW slice with every element's
// StartLine/EndLine populated -- resolved ONCE, together, here, exactly
// matching §22.1.1's own "no second pass, by construction" requirement: a
// Narvi verdict posts once, as a single typed payload with every finding
// already present, so there is no "later" resolution to defer to.
//
// diff == "" short-circuits to returning findings completely unchanged
// (every element's StartLine/EndLine already at their own zero-value
// "unanchored" default, reviewpost.BuildFindings never sets them) --
// nothing to match against and nothing worth asking the relocation LLM
// about either; resolver may safely be nil in this case too (the caller
// -- httpapi.PostReviewVerdict -- never constructs one when diff-fetching
// itself is unavailable). This mirrors §22.1.1's own "no diff, no
// position" fail-safe posture: a missing diff is exactly the same kind of
// safe degradation as a failed diff FETCH already is elsewhere in this
// codebase (internal/app/reviewcontext.Fetch's own "a failed fetch
// degrades gracefully" precedent).
//
// Per finding: reviewpost.MatchPosition (pure, no I/O) is tried FIRST;
// only on a failed match (both fields still 0) is resolver.Resolve
// consulted -- §22.1.1: "a failed match triggers one small... call", never
// the reverse, and never both attempted regardless of the first outcome.
func ResolveAll(ctx context.Context, resolver *Resolver, findings []reviewpost.Finding, diff string) []reviewpost.Finding {
	if diff == "" || len(findings) == 0 {
		return findings
	}

	logger := platform.Logger(ctx)
	out := make([]reviewpost.Finding, len(findings))
	copy(out, findings)

	for i := range out {
		startLine, endLine := reviewpost.MatchPosition(out[i].FilePath, out[i].Description, diff)
		if startLine != 0 {
			out[i].StartLine = startLine
			out[i].EndLine = endLine
			continue
		}

		if resolver == nil {
			// No relocation fallback configured (e.g. no usable LLM
			// credential at all) -- stays unanchored, logged once so an
			// operator can see the fallback never even attempted, distinct
			// from "attempted and failed".
			logger.Info("findingposition: pure match failed and no relocation resolver configured, leaving finding unanchored",
				"file_path", out[i].FilePath, "identity_hash", out[i].IdentityHash)
			continue
		}

		relocStart, relocEnd := resolver.Resolve(ctx, out[i].FilePath, out[i].Description, diff)
		out[i].StartLine = relocStart
		out[i].EndLine = relocEnd
	}

	return out
}
