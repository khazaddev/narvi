package d

import (
	_ "github.com/narvidev/narvi/internal/domain/license"
	"testing"
)

// TestNoExemptionNeeded proves the _test.go carve-out: a test
// constructing a registry (or, here, merely importing the licence
// domain) is not a production decision point -- demotionsweep.skipFile's
// own identical reasoning, applied to this analyzer.
func TestNoExemptionNeeded(t *testing.T) {}
