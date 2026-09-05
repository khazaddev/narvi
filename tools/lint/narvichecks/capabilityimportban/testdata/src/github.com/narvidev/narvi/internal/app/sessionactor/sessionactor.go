package sessionactor

import (
	_ "github.com/narvidev/narvi/internal/app/capability" // want `importing "github.com/narvidev/narvi/internal/app/capability" is banned outside the composition root`
)

// transact stands for the real session actor's own transaction-commit
// path -- must never be able to reach the capability registry, since a
// licence state must never become an input to what an actor commits.
func transact() {}
