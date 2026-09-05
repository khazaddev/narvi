package shadowscm

import (
	_ "github.com/narvidev/narvi/internal/domain/license" // want `importing "github.com/narvidev/narvi/internal/domain/license" is banned outside the composition root`
)

// isLive stands for the real shadow-mode transport decorator's own
// suppression decision -- this analyzer exists specifically so nothing
// in a package shaped like this one can ever import the licence domain
// at all, regardless of whether the import is actually used for
// anything (docs/design/boundaries-design.md §1.4).
func isLive() bool {
	return false
}
