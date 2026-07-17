package turn

// QueueEntry is the minimal per-turn info HasInFlightTurn and
// NextToDispatch need. ID is generic so this package stays free of any
// concrete identifier type (no uuid.UUID, no pgtype.UUID — domain has zero
// external dependencies, §1); the caller supplies whatever ID type its
// store layer uses.
type QueueEntry[ID any] struct {
	ID     ID
	Status State
}

// HasInFlightTurn reports whether turns (an ordered slice for one session)
// contains a Dispatched or Processing turn — the domain-layer mirror of
// the DB-enforced invariant `turns_one_processing_per_session` (§3.3:
// "Exactly one processing per session"). Dispatched counts as in-flight
// too: it already occupies the session's one dispatch slot, even though
// no sandbox has started executing it yet.
func HasInFlightTurn[ID any](turns []QueueEntry[ID]) bool {
	for _, t := range turns {
		if t.Status == StateDispatched || t.Status == StateProcessing {
			return true
		}
	}
	return false
}

// NextToDispatch returns the oldest Pending turn that should be dispatched
// next, or (zero value, false) if none should be dispatched right now —
// either because a turn is already in flight (HasInFlightTurn) or there is
// no Pending turn waiting.
//
// turns MUST already be ordered oldest-first (e.g. `ORDER BY created_at`);
// this is a pure function over whatever order the caller supplies, and
// picking "oldest" is simply "first Pending entry encountered" — §3.3
// gives no other tie-break rule, so oldest-first is the natural,
// uncontroversial choice.
func NextToDispatch[ID any](turns []QueueEntry[ID]) (ID, bool) {
	var zero ID

	if HasInFlightTurn(turns) {
		return zero, false
	}

	for _, t := range turns {
		if t.Status == StatePending {
			return t.ID, true
		}
	}

	return zero, false
}
