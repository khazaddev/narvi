package execimportban_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/narvidev/narvi/tools/lint/narvichecks/execimportban"
)

// TestAnalyzer proves the analyzer fires on an "os/exec" import outside
// every allowed tree (package "a"), and stays silent for the three real
// allowed trees (internal/adapters/outbound/rwx, internal/sandboxagent/
// boot, cmd/sandbox-agent).
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, execimportban.Analyzer,
		"a",
		"internal/adapters/outbound/rwx",
		"internal/sandboxagent/boot",
		"cmd/sandbox-agent",
	)
}
