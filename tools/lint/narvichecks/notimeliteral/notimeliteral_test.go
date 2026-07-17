package notimeliteral_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/khazaddev/narvi/tools/lint/narvichecks/notimeliteral"
)

// TestAnalyzer proves the analyzer fires on a time.Duration unit literal
// outside internal/platform (package "a"), stays silent on code that
// doesn't select one of the forbidden constants (package "b"), and stays
// silent inside internal/platform, the one place the convention permits
// literals (package "internal/platform").
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, notimeliteral.Analyzer, "a", "b", "internal/platform")
}
