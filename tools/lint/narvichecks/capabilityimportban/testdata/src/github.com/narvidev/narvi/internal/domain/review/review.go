package review

import (
	_ "github.com/narvidev/narvi/extension" // want `importing "github.com/narvidev/narvi/extension" is banned outside the composition root`
)

// Verdict stands for the real review-verdict domain type -- a pure
// domain package must never import the module-facing façade either: the
// ban is on all three capability-decision packages equally, not merely
// on the two lower-level ones.
type Verdict struct{}
