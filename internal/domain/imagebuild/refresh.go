package imagebuild

// NeedsRefresh implements §19.2's own freshness-pump comparison: true when
// ANY repo's current default-branch tip SHA (resolved by the caller, e.g.
// via ports.SourceControl.ResolveBranchSHA against a new platform-level
// GitHub credential) differs from that repo's own built SHA (the
// image_builds row's own built_repo_shas, recorded at the moment this
// fingerprint's image last successfully built or refreshed).
//
// Pure and total per §11: no I/O, no time.Now(), no randomness -- just a
// map comparison. A repo present in current but ABSENT from built (a repo
// added to the session's own repo set since the image last built --
// unreachable in practice today, since the fingerprint itself is keyed on
// the exact repo set, but handled here defensively rather than assumed
// impossible) is treated as needing refresh: there is no recorded SHA to
// compare against, so the safe assumption is "stale until proven
// otherwise" -- the same safe-default reasoning
// internal/sandboxagent/boot.ComputeWorkspaceMoved already uses for a
// repo absent from ITS OWN comparison map (§19.4).
func NeedsRefresh(built, current map[string]string) bool {
	for name, currentSHA := range current {
		builtSHA, ok := built[name]
		if !ok || builtSHA != currentSHA {
			return true
		}
	}
	return false
}
