package turn

// Summary is the minimal per-turn view a caller deriving SESSION-level
// status (internal/domain/session's DeriveStatus) needs: this turn's
// Status, plus — meaningful only when Status is Failed or Cancelled — the
// FailureReason its (from, trigger) transition implied, as produced by
// DeriveFailureReason. For any non-terminal Status (or StateCompleted),
// FailureReason is the zero value ("") and carries no meaning.
type Summary struct {
	Status        State
	FailureReason FailureReason
}
