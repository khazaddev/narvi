package intent

import "time"

// Source values for IntentDecisionRecord.Source (§18.4) -- a WIDER enum
// than ports.IntentDecision.Source (which is only "classifier" |
// "fallback"): "explicit" covers a surface that already deterministically/
// architecturally knows the answer without ever calling Classify at all
// (e.g. a human's own explicit plan/build toggle on the web UI).
const (
	RecordSourceClassifier = "classifier"
	RecordSourceExplicit   = "explicit"
	RecordSourceFallback   = "fallback"
)

// The two DecidedAtStage values (§18.4): some surfaces have the full
// input text at session creation, others (e.g. web's own warm-on-type
// path) only at the first real prompt.
const (
	DecidedAtStageCreate      = "create"
	DecidedAtStageFirstPrompt = "first_prompt"
)

// MaxReasoningLength bounds IntentDecisionRecord.Reasoning (§18.4:
// "truncated to a bounded length -- never rejected outright for being
// long, just cut off"). Not specified in the plan; chosen as 2000
// characters -- generous enough to keep a genuinely useful audit trail of
// the classifier's own reasoning (a few sentences) while still bounding a
// JSONB column value stored once per session, forever, against a
// pathological or adversarial-length model response.
const MaxReasoningLength = 2000

// TruncateReasoning cuts s to at most MaxReasoningLength runes (never
// bytes -- a byte-based cut could split a multi-byte rune and produce
// invalid UTF-8). s already within the bound is returned unchanged.
func TruncateReasoning(s string) string {
	r := []rune(s)
	if len(r) <= MaxReasoningLength {
		return s
	}
	return string(r[:MaxReasoningLength])
}

// IntentDecisionRecord is one row's worth of §18.4's per-session routing
// decision record -- the whole shape persisted into sessions.
// intent_decision (JSONB), minus session_id (redundant with the row's own
// PK, per §18.4's own instruction). Confidence/Reasoning/CostUSD are
// pointers because they are genuinely nullable/optional per §18.4:
// Confidence and Reasoning only ever populated for Source ==
// RecordSourceClassifier; CostUSD populated only when the real cost of
// the classifier's own LLM call is known (omitted, never guessed,
// whenever it isn't).
//
// Reasoning is stored here for audit but MUST NEVER be rendered on any
// Slack/Linear/GitHub-facing surface by default (§18.4) -- callers that
// read this record back for display purposes must respect that on their
// own; this type itself has no rendering behavior to get that wrong.
//
// "IntentDecisionRecord" is §18.4's own fixed name for this exact shape,
// quoted verbatim in the technical plan -- kept byte-for-byte rather than
// renamed to silence the package-name-stutter check below.
//
//nolint:revive // stutters intentionally; see comment above.
type IntentDecisionRecord struct {
	Surface        string    `json:"surface"`
	Source         string    `json:"source"`
	Target         string    `json:"target"`
	Mode           string    `json:"mode"`
	Confidence     *string   `json:"confidence,omitempty"`
	Reasoning      *string   `json:"reasoning,omitempty"`
	DecidedAt      time.Time `json:"decided_at"`
	DecidedAtStage string    `json:"decided_at_stage"`
	CostUSD        *float64  `json:"cost_usd,omitempty"`
}
