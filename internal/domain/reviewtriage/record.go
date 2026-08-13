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
}

// NewDecisionRecord builds a DecisionRecord from decision (Decide's own
// output), cfg (the resolved per-repo config Decide was called with), and
// finalDepth (decision.Depth after Floor has been applied, if this is a
// re-review path -- callers with no floor to apply pass decision.Depth
// itself unchanged).
func NewDecisionRecord(decision Decision, cfg Config, finalDepth ReviewDepth) DecisionRecord {
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
	}
}
