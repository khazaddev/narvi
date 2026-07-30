package opencode

import (
	"context"
	"net/http"
	"net/url"
)

// forceCompaction implements §7.2's own "force a compaction via the
// endpoint that actually works" design point: POSTs {"providerID",
// "modelID"} (from model) to POST /session/{id}/summarize, the always-
// available summarization endpoint this Step's own live research settled
// on after confirming the plan's originally-proposed sibling endpoint
// (POST /session/{id}/compact — see types.go's compactionPart doc comment)
// still returns "not available yet" on the pinned OpenCode version.
//
// PRECONDITION: model must be non-nil — the caller's responsibility (see
// resolveModelForced, session.go, the ONLY resolver this Step's own
// callers, adapter.go's finalizeOrRecoverFromOverflow/
// attemptCompactionRetry, ever pass here). VERIFIED live via GET /doc: the
// real /summarize requestBody schema requires BOTH providerID and modelID
// (unlike prompt_async's own optional "model" field) — an empty {} body
// independently reproduced live to return HTTP 400 {"name":"BadRequest",
// "data":{"message":"Missing key\n  at [\"providerID\"]"}} against the
// pinned OpenCode 1.17.15 binary.
//
// Bounded by a.summarizeTimeout (platform.Timeouts.
// OpenCodeSummarizeTimeout, 120s in production), routed through
// doJSONTimeout directly — NOT doJSON, which would incorrectly apply
// a.requestTimeout (30s) instead (§7.2 Finding 3): this Step's own live
// timing observed a real /summarize call (a real, if small, conversation)
// complete in ~2s, but a large real-world context that actually triggered
// a ContextOverflowError in the first place could plausibly take far
// longer, and silently truncating that at 30s would turn a legitimately
// still-in-progress compaction into a spurious extra failure.
//
// On success, OpenCode's own response is a bare JSON boolean (VERIFIED
// live: `true`) — decoded into a throwaway *bool; its actual value is
// irrelevant to every caller here, only whether the call errored at all
// (a non-2xx status or a transport failure).
//
// Called while ts.compacting is true (set by the caller, adapter.go) —
// see sse.go's dispatchEvent doc comment and this Step's own VERIFIED LIVE
// finding for why: capturing real GET /event traffic during a live
// /summarize call confirmed this HTTP call genuinely blocks until
// compaction is fully done (the response only returns after the full wave
// of compaction-internal SSE traffic below has already streamed), but
// WHILE it is in flight, the SAME OpenCode session emits a full extra
// wave of message.updated (a NEW assistant message with info.mode==
// "compaction", info.agent=="compaction", info.summary==true) /
// message.part.updated / session.status / session.idle / session.compacted
// (a wire event type not otherwise modeled in types.go, since nothing
// here needs to react to it directly — ts.compacting is the guard, not a
// parsed field of this event) / session.updated / session.diff events on
// the SAME global /event stream this adapter's own persistent SSE loop is
// already reading — for the SAME sessionID as the turn that triggered the
// overflow.
func (a *Adapter) forceCompaction(ctx context.Context, sessionID string, model *promptModelRef) error {
	body := summarizeRequest{ProviderID: model.ProviderID, ModelID: model.ModelID}
	path := "/session/" + url.PathEscape(sessionID) + "/summarize"
	var result bool
	return a.doJSONTimeout(ctx, a.summarizeTimeout, http.MethodPost, path, body, &result)
}
