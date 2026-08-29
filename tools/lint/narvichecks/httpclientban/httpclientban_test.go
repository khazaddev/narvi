package httpclientban_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/khazaddev/narvi/tools/lint/narvichecks/httpclientban"
)

// TestAnalyzer proves: a plain package outside every allowed tree is
// reported for both a construction-shaped (http.DefaultClient) and an
// invocation-shaped (http.NewRequestWithContext) reference, while server-
// side net/http in the SAME file is untouched (package "a"); the fully
// allowed trees stay silent on every banned symbol (internal/adapters/
// outbound/githubapi, internal/sandboxagent/boot, cmd/sandbox-agent);
// cmd/control-plane, the composition root, stays silent on CONSTRUCTION
// but is still reported for INVOCATION; and the pinned baseline file
// (internal/adapters/inbound/auth/callback.go) is silent while an
// unaudited SIBLING file in the very same package (other.go) is still
// reported -- proving the baseline is a per-file allowance, never a
// per-directory one.
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, httpclientban.Analyzer,
		"a",
		"internal/adapters/outbound/githubapi",
		"internal/sandboxagent/boot",
		"cmd/sandbox-agent",
		"cmd/control-plane",
		"internal/adapters/inbound/auth",
	)
}
