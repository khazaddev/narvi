package upload

// FailureReason is this package's own pure domain vocabulary for why an
// upload failed (§28.4's four values). Deliberately NOT
// sqlcgen.ArtifactFailureReason (an adapter-layer generated type):
// domain packages never import adapters/* (see doc.go), so
// internal/adapters/outbound/postgres converts between the two at ITS own
// boundary -- a plain string cast, since the values are defined to match
// exactly, one for one.
type FailureReason string

// The complete set of §28.4 failure reasons.
const (
	// FailureReasonSizeExceeded means the declared or actual size exceeds
	// the per-file cap (MaxUploadBytes).
	FailureReasonSizeExceeded FailureReason = "size_exceeded"
	// FailureReasonQuotaExceeded means the session's own running total
	// (this upload included) exceeds the per-session cap
	// (MaxSessionUploadBytes).
	FailureReasonQuotaExceeded FailureReason = "quota_exceeded"
	// FailureReasonVerificationFailed means confirm's Stat call found the
	// object missing, or its actual size did not match what was declared
	// at mint.
	FailureReasonVerificationFailed FailureReason = "verification_failed"
	// FailureReasonAbandoned means the abandonment sweep found this row
	// still 'pending' past UploadPendingSweepAfter -- the client minted
	// and never transferred/confirmed.
	FailureReasonAbandoned FailureReason = "abandoned"
)

// EvaluateUploadSize is the pure §28.4 size/quota check shared by BOTH
// mint (a fast-fail courtesy against the caller's own DECLARED size) and
// confirm (the enforcement of record: the SAME check, re-run against the
// object's ACTUAL stat'd size, "re-checked now" -- §28.4's own words,
// closing the two-mints-racing-past-the-cap window mint-time checks alone
// cannot).
//
// sizeBytes is the size to evaluate (declared at mint, actual at
// confirm). sessionTotalBytes is the session's own running total
// EXCLUDING this upload (SUM(size_bytes) over its other pending+ready
// rows) -- callers add sizeBytes to it here, never before calling, so the
// same sessionTotalBytes value works whether this upload's own row
// already exists (confirm; excluded because it is the row this method
// is not counted twice for) or does not yet exist (mint).
//
// Returns ("", true) when both checks pass. Otherwise returns the FIRST
// violated reason (size checked before quota, matching §28.4's own
// listed order) and false -- ok is the field to branch on; reason is only
// meaningful when ok is false.
func EvaluateUploadSize(sizeBytes, maxUploadBytes, sessionTotalBytes, maxSessionUploadBytes int64) (reason FailureReason, ok bool) {
	if sizeBytes > maxUploadBytes {
		return FailureReasonSizeExceeded, false
	}
	if sessionTotalBytes+sizeBytes > maxSessionUploadBytes {
		return FailureReasonQuotaExceeded, false
	}
	return "", true
}
