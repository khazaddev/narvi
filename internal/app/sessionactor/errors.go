package sessionactor

import "errors"

var (
	// ErrSessionActorElsewhere is returned by Registry.GetOrSpawn when
	// this process failed to win the session's Postgres advisory lock
	// (pg_try_advisory_lock returned false): another pod -- or another
	// goroutine in this same process, though Registry's own mutex should
	// already rule that case out -- currently owns this session's actor.
	// §2's fail-fast requirement: GetOrSpawn never blocks waiting for the
	// lock, so a caller in a later Step can route the request to whichever
	// pod actually holds it instead of hanging.
	ErrSessionActorElsewhere = errors.New("sessionactor: session actor is owned elsewhere")

	// ErrStaleEpoch is returned by Actor.transact when the epoch read back
	// from the session row (inside the write's own transaction, via
	// GetSessionActorEpochForUpdate) no longer matches the epoch this
	// Actor remembers from its own hydration -- proof a newer actor has
	// since taken over this session (§2: "writes with a stale epoch
	// fail. A zombie actor on an old pod can never corrupt state"). This
	// is fatal to the Actor that receives it: see Actor.run, which evicts
	// itself from the Registry and stops processing further commands the
	// moment this error surfaces from a command handler.
	ErrStaleEpoch = errors.New("sessionactor: stale actor epoch")

	// ErrActorStopped is returned by Actor.Send when the actor's mailbox
	// loop has already exited (idle-TTL eviction, ErrStaleEpoch, or
	// process shutdown) -- the caller should treat this the same as
	// "no live actor", i.e. call Registry.GetOrSpawn again to hydrate a
	// fresh one if the work still needs doing.
	ErrActorStopped = errors.New("sessionactor: actor has stopped")
)
