package reviewtriage

// DecisionRecord is Step 68's own version of §18.4's per-session routing
// decision record (IntentDecisionRecord, internal/domain/intent/
// record.go) -- persisted verbatim, write-once, onto
// turns.review_depth_decision (migrations/
// 000083_turns_review_depth_decision.up.sql, JSONB) by whichever
// review-turn-creation path just called Decide (and, for a re-review,
// Floor). See that migration's own doc comment for why this rides a
// per-TURN column rather than a per-session one the way
// IntentDecisionRecord does.
//
// Every field is a plain, JSON-tagged value (mirrors IntentDecisionRecord's
// own shape) -- built by the app-layer caller (internal/app/reviewtriage)
// from a Decision plus the two facts Decision itself does not carry
// (Config.Mode, and whether Floor actually raised the fresh result).
type DecisionRecord struct {
	Depth                string   `json:"depth"`
	Reason               string   `json:"reason"`
	MatchedSensitiveTags []string `json:"matchedSensitiveTags,omitempty"`
	ChangedLines         int      `json:"changedLines"`
	DistinctRoots        int      `json:"distinctRoots"`
	Mode                 string   `json:"mode"`
	// Floored reports whether §24's re-review floor (Floor, depth.go) is
	// what actually determined the final Depth above -- true means the
	// fresh Decide result was itself overridden by a higher-ranked PRIOR
	// depth on record; false means Depth is exactly what Decide computed
	// from this turn's own fresh signals alone.
	Floored bool `json:"floored"`

	// NarviAuthored/AuthoringModel (§26.3's own "provenance: Narvi-
	// authored vs human, and the authoring model" signal) are captured
	// here but NEVER consulted by Decide -- v1's own five rules (doc.go)
	// do not include provenance. Recorded now so Step 69 (§26.4's
	// cross-family counter-review, "the family comes from provenance,
	// the tier from depth") does not have to re-derive it later.
	// AuthoringModel is empty whenever NarviAuthored is false, or when a
	// Narvi-authored session never had an explicit build model set.
	NarviAuthored  bool   `json:"narviAuthored"`
	AuthoringModel string `json:"authoringModel,omitempty"`

	// ResolvedModelID/ResolvedEffort (D4, nice-to-have adversarial-review
	// fix) are ModelAndEffort's own (modelID, effort) return values
	// (modeleffort.go), the ACTUAL override this turn's own creation call
	// requested -- recorded here so a maintainer inspecting turns.
	// review_depth_decision can see, for THIS specific turn, whether the
	// deep-tier model override actually fired (ResolvedModelID non-empty)
	// or silently stayed inert (ResolvedModelID empty on a deep-routed
	// turn -- platform.Config.ReviewModelDeep was never configured for
	// this deployment, ModelAndEffort's own doc comment). Both empty on
	// the light path (ModelAndEffort returns nil, nil for anything other
	// than DepthDeep). ResolvedEffort is "high" whenever Depth is deep,
	// regardless of ResolvedModelID.
	ResolvedModelID string `json:"resolvedModelId,omitempty"`
	ResolvedEffort  string `json:"resolvedEffort,omitempty"`
}

// Provenance is internal/app/reviewtriage.ResolveProvenance's own return
// value (a SEPARATE call from ComputeDecision -- ComputeDecision's own
// three return values are Decision, Config, and, as of D1's adversarial-
// review fix, the prior review_path ReviewDepth; provenance is resolved
// independently, by its own dedicated function, not bundled into that
// tuple) -- see DecisionRecord.NarviAuthored/AuthoringModel's own doc
// comment for why this is captured but not routed on.
type Provenance struct {
	NarviAuthored  bool
	AuthoringModel string
}

// NewDecisionRecord builds a DecisionRecord from decision (Decide's own
// output), cfg (the resolved per-repo config Decide was called with),
// finalDepth (decision.Depth after Floor has been applied, if this is a
// re-review path -- callers with no floor to apply pass decision.Depth
// itself unchanged), provenance (internal/app/reviewtriage's own
// best-effort authorship lookup), and resolvedModelID/resolvedEffort (D4,
// nice-to-have adversarial-review fix -- ModelAndEffort's own two return
// values, computed by the caller from the SAME finalDepth passed here;
// nil for both on the light path, mirroring ModelAndEffort's own "both
// nil except on DepthDeep" contract).
func NewDecisionRecord(decision Decision, cfg Config, finalDepth ReviewDepth, provenance Provenance, resolvedModelID, resolvedEffort *string) DecisionRecord {
	tags := make([]string, len(decision.MatchedSensitiveTags))
	for i, t := range decision.MatchedSensitiveTags {
		tags[i] = string(t)
	}
	var modelID, effort string
	if resolvedModelID != nil {
		modelID = *resolvedModelID
	}
	if resolvedEffort != nil {
		effort = *resolvedEffort
	}
	return DecisionRecord{
		Depth:                string(finalDepth),
		Reason:               string(decision.Reason),
		MatchedSensitiveTags: tags,
		ChangedLines:         decision.ChangedLines,
		DistinctRoots:        decision.DistinctRoots,
		ResolvedModelID:      modelID,
		ResolvedEffort:       effort,
		Mode:                 string(resolveMode(cfg.Mode)),
		Floored:              finalDepth != decision.Depth,
		NarviAuthored:        provenance.NarviAuthored,
		AuthoringModel:       provenance.AuthoringModel,
	}
}
