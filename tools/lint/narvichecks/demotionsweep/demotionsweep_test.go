package demotionsweep_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/khazaddev/narvi/tools/lint/narvichecks/demotionsweep"
)

// TestAnalyzer proves the analyzer fires on a new caller that flips
// live_egress_enabled without sweeping (package "a"), stays narrow enough
// to ignore other repo settings and a same-named declaration that is
// never called (packages "a" and "b"), and stays silent in the two
// packages permitted to make the call: "internal/app/seed" (pairs a
// true->false transition with the demotion sweep) and
// "internal/app/shadowoperator" (a promotion-only, false->true caller
// that owes no sweep at all -- see demotionsweep.go's own doc comment).
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, demotionsweep.Analyzer, "a", "b", "internal/app/seed", "internal/app/shadowoperator")
}
