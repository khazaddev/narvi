package gitstate

// This file implements the small, pure "which Trigger does a real,
// caller-observed git outcome correspond to" mapping functions the boot
// sequence needs (§3.4) -- doc.go's own words for exactly this gap:
// "actually running git and feeding the outcomes back into this machine is
// a later Step's concern (sandbox-agent...), not this one's". That later
// Step (Step 29, "gitstate in-sandbox") is this one; internal/sandboxagent/
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
