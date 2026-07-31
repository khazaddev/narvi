package review

// RiskLevel is the reviewer's own overall risk assessment for a PR — one
// column of the risk-map verdict table mockups.html shows (Area × Risk ×
// Assessment). Exactly three values (see doc.go's design call #1 for why
// not four): the same low/medium/high vocabulary the mockup already uses
// both for the overall verdict chip ("review: low risk", "review: medium
// risk") and for individual finding severities.
type RiskLevel string

// The three RiskLevel values. The zero value ("") is deliberately not one
// of them — see baselineFromRisk (shippable.go) for how an unrecognized
// value is handled.
const (
	// RiskLevelLow is the reviewer finding nothing that itself warrants
	// human gating.
	RiskLevelLow RiskLevel = "low"
	// RiskLevelMedium is the reviewer finding something worth a human's
	// attention, short of RiskLevelHigh.
	RiskLevelMedium RiskLevel = "medium"
	// RiskLevelHigh is the reviewer's most severe defined risk tier. Note
	// that RiskLevelHigh alone (with both floors clean) still only raises
	// Shippable to ShippableNeedsHuman, never ShippableBlock — see
	// baselineFromRisk's own doc comment for why Block is reserved for the
	// premise floor instead.
	RiskLevelHigh RiskLevel = "high"
)
