package findingposition

import (
	"context"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/platform"
)

// resolveOneRelocation calls resolver.Resolve for ONE finding, scoping the
// prompt to filePath's own diff section (reviewpost.SliceFileDiff) and
// then independently verifying the model's own returned StartLine/EndLine
// actually fall inside filePath's own new-file line bounds
// (reviewpost.FileNewLineBounds) before ever trusting them -- two
// independent safeguards against the SAME failure mode: a relocation
// answer landing in the WRONG FILE. Before this fix, ResolveAll passed the
// WHOLE multi-file diff into Resolve and assigned its answer verbatim with
// zero bounds checking, so a line number belonging to a different file in
// the diff (or not existing in the target file at all) was written
// straight into the finding -- exactly the failure mode MatchPosition's
// own filePath argument exists to prevent on the pure-match path, dropped
// entirely on this fallback path. A relocation answer failing the bounds
// check is rejected to (0, 0), never used partially or "close enough" --
// §22.1.1's own "0, never a guess" mandate, extended here to match
// MatchPosition's own identical posture.
//
// When filePath has NO lines in this diff at all (FileNewLineBounds'
// ok=false), the relocation call is skipped entirely: there is no bounds
// to check ANY answer against, so no answer could ever be verified as
// correct -- mirrors MatchPosition's own "unknown file -> nothing to
// match" degradation exactly, and saves an LLM call with a foregone
// outcome.
func resolveOneRelocation(ctx context.Context, resolver *Resolver, filePath, description, diff string) (startLine, endLine int) {
	logger := platform.Logger(ctx)

	minLine, maxLine, ok := reviewpost.FileNewLineBounds(diff, filePath)
	if !ok {
		return 0, 0
	}

	fileDiff := reviewpost.SliceFileDiff(diff, filePath)
	relocStart, relocEnd := resolver.Resolve(ctx, filePath, description, fileDiff)
	if relocStart == 0 {
		return 0, 0
	}

	if relocStart < minLine || relocEnd > maxLine || relocStart > relocEnd {
		logger.Warn("findingposition: relocation llm returned a line range outside this file's own diff bounds, rejecting to unanchored",
			"file_path", filePath, "returned_start", relocStart, "returned_end", relocEnd, "file_min", minLine, "file_max", maxLine)
		return 0, 0
	}

	return relocStart, relocEnd
}

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
//
// The whole loop's own relocation-fallback calls (never the pure-match
// step, which is pure/no I/O and always runs for every finding regardless)
// are bounded by ONE aggregate context.WithTimeout,
// timeouts.FindingPositionResolveAllTimeout -- serial, one blocking LLM
// call per unmatched finding, with no aggregate deadline, means N
// unmatched findings could block httpapi.PostReviewVerdict's own
// synchronous, pre-transaction verdict-POST handler for N times a single
// call's own budget; a client cancelling mid-wait would then lose the
// WHOLE verdict, not just its position data. Once the aggregate budget is
// exhausted, every NOT-yet-processed finding is left at its own pure-match
// result (0, 0 if that also failed) rather than blocking further -- a
// safe, honest degradation, never a guess.
func ResolveAll(ctx context.Context, resolver *Resolver, findings []reviewpost.Finding, diff string, timeouts platform.Timeouts) []reviewpost.Finding {
	if diff == "" || len(findings) == 0 {
		return findings
	}

	logger := platform.Logger(ctx)
	out := make([]reviewpost.Finding, len(findings))
	copy(out, findings)

	resolveCtx, cancel := context.WithTimeout(ctx, timeouts.FindingPositionResolveAllTimeout)
	defer cancel()

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

		if resolveCtx.Err() != nil {
			logger.Warn("findingposition: aggregate relocation budget exhausted, leaving remaining findings unanchored",
				"file_path", out[i].FilePath, "identity_hash", out[i].IdentityHash)
			continue
		}

		out[i].StartLine, out[i].EndLine = resolveOneRelocation(resolveCtx, resolver, out[i].FilePath, out[i].Description, diff)
	}

	return out
}
