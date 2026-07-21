package contractdrift

// Snapshot is one repo's resolved (RepoSHA, ContractsFingerprint) pair at
// some point in time -- either the last one persisted (internal/adapters/
// outbound/postgres's own ContractDriftStore row), or the one just
// resolved for the current spawn/restore being checked
// (app/sessionactor's own checkContractDrift, contractdrift.go).
type Snapshot struct {
	// RepoSHA is the repo's own resolved commit SHA at the moment this
	// Snapshot was taken. The zero value ("") is a sentinel meaning "no
	// Snapshot has ever been recorded for this repo" -- HasDrifted's own
	// first truth-table row below relies on this exact meaning; it is
	// never a real, resolved commit SHA's own value (a real SHA is never
	// empty).
	RepoSHA string

	// ContractsFingerprint is contractdrift.Fingerprint's own output over
	// the repo's configured contracts directory listing at RepoSHA. ""
	// means "no contracts directory existed at RepoSHA at all" -- a
	// DIFFERENT, caller-level sentinel than Fingerprint's own "hash of an
	// empty (but existing) directory" case (Fingerprint's own doc comment
	// explains why Fingerprint itself never needs to distinguish the two;
	// this field's own "" is how a caller records "didn't exist" once it
	// already knows that, from ports.SourceControl.ResolveContractsFingerprint's
	// own exists=false return).
	ContractsFingerprint string
}

// HasDrifted implements §14.3's own drift signal: "If a real backend
// endpoint changes without the contract being updated, this doesn't block
// anything -- it feeds the handoff sentinel." Given the last recorded
// Snapshot for a repo (previous) and the one just resolved for the
// current spawn/restore (current), returns whether THIS repo has drifted
// since previous was recorded.
//
// Truth table, in the exact order evaluated:
//
//  1. previous.RepoSHA == "" (no prior snapshot recorded yet -- this is
//     the FIRST time this repo has ever been seen by this mechanism) ->
//     false, nothing to compare against yet. Mirrors internal/app/
//     reconciler's own "first sighting: record, don't act" precedent
//     (reconciler.go's own ReconcileOnce doc comment: "Reaping on first
//     sighting would kill a legitimate, in-flight spawn") -- here, acting
//     on a first sighting would mean flagging drift against a Snapshot
//     that never really existed, which is exactly as wrong.
//  2. current.ContractsFingerprint == "" (no contracts directory exists
//     at the CURRENT ref) -> false, nothing to drift FROM: a repo that
//     currently has no declared contract at all cannot have a stale one.
//  3. previous.RepoSHA == current.RepoSHA (the repo itself has not
//     changed since the last snapshot) -> false, can't have drifted --
//     nothing moved.
//  4. Otherwise (the repo's own SHA HAS changed since previous) -> true
//     iff previous.ContractsFingerprint == current.ContractsFingerprint:
//     the repo evolved but its own declared contract did not. This is
//     the actual drift signal §14.3 names.
//
// The easiest row to get backwards: if the repo changed AND the contracts
// fingerprint ALSO changed, that is NOT drift -- the contract was
// properly updated alongside the backend, which is the whole point of
// versioning the mock as a reviewed repo artifact (§14.3: "authored once
// and reviewed like code"). Row 4's "iff previous == current" condition
// already encodes this correctly (two different fingerprints compare
// unequal, so HasDrifted returns false), but it is called out here
// explicitly because a naive "repo changed -> flag it" implementation
// would get this exact case wrong.
func HasDrifted(previous, current Snapshot) bool {
	if previous.RepoSHA == "" {
		return false
	}
	if current.ContractsFingerprint == "" {
		return false
	}
	if previous.RepoSHA == current.RepoSHA {
		return false
	}
	return previous.ContractsFingerprint == current.ContractsFingerprint
}
