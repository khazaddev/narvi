package shadowcompare

import "time"

// TurnSnapshot is one turn's own comparable state -- deliberately NOT
// sqlcgen.Turn (this package stays adapter-independent, §11): the
// caller (internal/adapters/inbound/httpapi) converts from the real
// store rows.
type TurnSnapshot struct {
	TurnID       string
	SessionID    string
	ModelID      *string
	Effort       *string
	Status       string
	CreatedAt    time.Time
	DispatchedAt *time.Time
	CompletedAt  *time.Time
}

// DurationSeconds returns CompletedAt minus DispatchedAt, in seconds, or
// nil when either is unset (the turn has not finished, or never
// dispatched at all) -- mirrors ShadowComparisonTurn.durationSeconds'
// own wire-contract doc comment exactly ("null until both are set").
func (s TurnSnapshot) DurationSeconds() *float64 {
	if s.DispatchedAt == nil || s.CompletedAt == nil {
		return nil
	}
	d := s.CompletedAt.Sub(*s.DispatchedAt).Seconds()
	return &d
}

// Report is Compare's own result -- both sides, side by side, for a human
// to read; this package renders no verdict of its own (no "A is better
// than B" -- that judgment belongs to the human this tool exists to
// inform, never automated).
type Report struct {
	TurnA TurnSnapshot
	TurnB TurnSnapshot
}

// Compare bundles a and b into one Report -- a pure, trivial function
// today (see package doc.go for why: this tool's own value is in
// ASSEMBLING and PRESENTING two real turns side by side, not in computing
// a clever verdict over them). Kept as a named function, not inlined at
// the call site, so a future comparison heuristic (e.g. flagging a large
// duration/cost delta) has one obvious place to land without changing
// this package's own public shape.
func Compare(a, b TurnSnapshot) Report {
	return Report{TurnA: a, TurnB: b}
}
