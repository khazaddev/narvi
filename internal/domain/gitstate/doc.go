// Package gitstate implements the pure decision logic for a sandbox's git
// boot sequence (§3.4, "Git state (inside sandbox, enforced by
// sandbox-agent)"):
//
//   - The explicit Transition(from, trigger) (to, error) table (state.go)
//     for "stash-if-dirty -> checkout session branch (create from base if
//     absent) -> stash pop", matching internal/domain/sandbox and
//     internal/domain/turn's house style: typed sentinel errors for
//     illegal transitions, never a bare zero-value State silently
//     accepted.
//   - IsTerminal and RequiresStashRecovery (state.go): the ten states are
//     named so that whether a stash currently exists unpopped is legible
//     from the state alone, without consulting which trigger produced it.
//     RequiresStashRecovery is the package's central correctness property:
//     §3.4 calls user working-tree edits "durable data -- losing them is a
//     P0", so a caller recovering from a crash or a failure must be able
//     to tell, from persisted state alone, whether there is a stash
//     sitting in the stash list that still needs manual recovery.
//   - NormalizeBranchName (branchname.go): the "branch names normalized
//     (lowercase) before push" clause of §3.4 -- lowercasing only, nothing
//     more.
//
// Every function here is pure per §11: no I/O, no time.Now(), no
// randomness. There is no Clock/duration concept in this package at all --
// unlike internal/domain/sandbox or internal/domain/turn, no state here is
// time-bounded; a boot sequence sits in a non-terminal state until some
// caller-observed outcome (a stash/checkout/pop attempt succeeding or
// failing) supplies the next trigger. This package also executes zero git
// commands and does zero shelling out of any kind -- it only decides WHAT
// state a boot sequence is in and what it transitions to next. Actually
// running git and feeding the outcomes back into this machine is a later
// Step's concern (sandbox-agent, per the plan's own phasing), not this
// one's.
//
// Two clauses of §3.4 are deliberately NOT modeled here, because each
// belongs to a different Step:
//
//   - "Image builds must snapshot a clean tree" is background for a later
//     image-build Step, not a boot-sequence concern -- nothing here
//     invents a state or transition for it.
//   - "Repo paths: multi-repo under /workspace/{name}, position 0 =
//     primary. Repos are always a list" is a workspace-layout/adapter
//     concern for whichever later Step actually clones repos on disk, not
//     a decision this pure state machine needs to make.
package gitstate
