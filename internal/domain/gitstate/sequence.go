package gitstate

// This file implements the small, pure "which Trigger does a real,
// caller-observed git outcome correspond to" mapping functions the boot
// sequence needs (§3.4) -- doc.go's own words for exactly this gap:
// "actually running git and feeding the outcomes back into this machine is
// a later Step's concern (sandbox-agent...), not this one's". That later
// Step (§3.4, "gitstate in-sandbox") is this one; internal/sandboxagent/
// gitclone.SyncAll is the real caller, running actual git subprocesses and
// feeding each one's outcome through the function here that names it,
// then through Transition itself.
//
// Each function below is a trivial, exhaustively-tested bool -> Trigger
// mapping. Kept as its own small, named, documented decision point --
// rather than inlined as a bare ternary at every real git call site -- so
// the SEQUENCING decision (which Trigger a given real-world outcome feeds
// into Transition) is explicit and independently testable in isolation
// from any real git I/O, exactly like every other decision this package
// makes. Pure per §11: no I/O, no time.Now(), no randomness.

// TriggerForDirtyCheck returns the Trigger a caller (a real `git status
// --porcelain` check, run once at the START of a repo's boot sequence,
// while it is in StateIdle) should feed to Transition: dirty is whether
// that check found any uncommitted change in the working tree.
func TriggerForDirtyCheck(dirty bool) Trigger {
	if dirty {
		return TriggerDirtyDetected
	}
	return TriggerCleanDetected
}

// TriggerForStash returns the Trigger a caller (a real `git stash push`
// attempt, made only after TriggerForDirtyCheck(true) already moved the
// machine into StateStashing) should feed to Transition, based on whether
// that attempt succeeded.
func TriggerForStash(succeeded bool) Trigger {
	if succeeded {
		return TriggerStashSucceeded
	}
	return TriggerStashFailed
}

// TriggerForCheckout returns the Trigger a caller (a real `git checkout`/
// `git checkout -b` attempt, made from either StateCheckingOutClean or
// StateCheckingOutWithStash) should feed to Transition, based on whether
// that attempt succeeded. The SAME Trigger name applies from either FROM
// state -- Transition itself (state.go) is what resolves the different
// destination each one lands in.
func TriggerForCheckout(succeeded bool) Trigger {
	if succeeded {
		return TriggerCheckoutSucceeded
	}
	return TriggerCheckoutFailed
}

// TriggerForPop returns the Trigger a caller (a real `git stash pop`
// attempt, made only from StatePoppingStash -- i.e. only when a stash was
// actually taken and checkout onto the session branch already succeeded)
// should feed to Transition, based on whether that attempt succeeded. A
// false here is §3.4's own P0 case: RequiresStashRecovery(StatePopFailed)
// is true, meaning a stash sits in the stash list, unpopped.
func TriggerForPop(succeeded bool) Trigger {
	if succeeded {
		return TriggerPopSucceeded
	}
	return TriggerPopFailed
}

// TriggerForFetch returns the Trigger a caller (a real, credentialed `git
// fetch origin <resolved-branch> <default-branch>` attempt, made from
// StateFetching -- the boot sequence's new real starting point, §19.3)
// should feed to Transition. fetchSucceeded is whether that attempt
// actually fetched the resolved target branch (gitstate.ResolveSessionBranch's
// own return value) -- NOT merely whether some other ref (e.g. the
// repo's default branch) was fetched successfully; see
// internal/sandboxagent/gitclone's own fetch-step doc comment for why a
// nonexistent-upstream branch (the common invented "narvi/<sessionID>"
// case) is fetchSucceeded=false even when the remote itself is perfectly
// reachable. degradeAllowed is §19.3's own non-negotiable policy input,
// already decided by the caller from real, already-available information
// BEFORE this function is ever called: true when the target branch either
// already exists locally (branchExistsLocally) or is "acceptable from
// HEAD" because repo.Branch was nil (an invented session branch, never
// named explicitly by the session) -- false when the session explicitly
// named a branch (repo.Branch != nil) that is neither local nor fetchable.
//
//   - fetchSucceeded true: TriggerFetchSucceeded, regardless of
//     degradeAllowed -- a successful fetch never needs the degrade policy
//     at all.
//   - fetchSucceeded false, degradeAllowed true: TriggerFetchFailedDegraded
//     -- "warm boot must never become network-dependent for liveness"
//     (§19.3): proceed on stale image state, landing on the SAME
//     destination (StateIdle) as a success, just under a different,
//     distinctly-named trigger so the caller can log the appropriate
//     warning.
//   - fetchSucceeded false, degradeAllowed false: TriggerFetchFailedFatal
//     -- §19.3's own non-negotiable rule: silently forking a same-named
//     branch at a stale base must never happen, so this is the one
//     outcome that fails the repo outright (StateFetchFailed) rather than
//     degrading.
func TriggerForFetch(fetchSucceeded, degradeAllowed bool) Trigger {
	if fetchSucceeded {
		return TriggerFetchSucceeded
	}
	if degradeAllowed {
		return TriggerFetchFailedDegraded
	}
	return TriggerFetchFailedFatal
}
