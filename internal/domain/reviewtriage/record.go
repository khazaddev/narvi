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
}

// Provenance is ComputeDecision's own third return value (internal/app/
// reviewtriage) -- see DecisionRecord.NarviAuthored/AuthoringModel's own
// doc comment for why this is captured but not routed on.
type Provenance struct {
	NarviAuthored  bool
	AuthoringModel string
}

// NewDecisionRecord builds a DecisionRecord from decision (Decide's own
// output), cfg (the resolved per-repo config Decide was called with),
// finalDepth (decision.Depth after Floor has been applied, if this is a
// re-review path -- callers with no floor to apply pass decision.Depth
// itself unchanged), and provenance (internal/app/reviewtriage's own
// best-effort authorship lookup).
func NewDecisionRecord(decision Decision, cfg Config, finalDepth ReviewDepth, provenance Provenance) DecisionRecord {
	tags := make([]string, len(decision.MatchedSensitiveTags))
	for i, t := range decision.MatchedSensitiveTags {
		tags[i] = string(t)
	}
	return DecisionRecord{
		Depth:                string(finalDepth),
		Reason:               string(decision.Reason),
		MatchedSensitiveTags: tags,
		ChangedLines:         decision.ChangedLines,
		DistinctRoots:        decision.DistinctRoots,
		Mode:                 string(resolveMode(cfg.Mode)),
		Floored:              finalDepth != decision.Depth,
		NarviAuthored:        provenance.NarviAuthored,
		AuthoringModel:       provenance.AuthoringModel,
	}
}
