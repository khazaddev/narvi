package nilhttpclient_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/narvidev/narvi/tools/lint/narvichecks/nilhttpclient"
)

// TestAnalyzer proves the check fires on a nil *http.Client in any
// argument position, stays silent for a real client, and does not fire
// for a nil passed to some other parameter type -- the narrowness that
// keeps it from becoming noise nobody reads.
func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), nilhttpclient.Analyzer, "a")
}
