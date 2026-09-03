package nakedgoroutine_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/narvidev/narvi/tools/lint/narvichecks/nakedgoroutine"
)

// TestAnalyzer proves the analyzer fires on a naked `go` statement (package
// "a"), stays silent on compliant code (package "b"), and fires inside
// _test.go files too (package "c") since §11 grants no test exemption for
// this rule.
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, nakedgoroutine.Analyzer, "a", "b", "c")
}
