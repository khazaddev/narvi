package capabilityimportban_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/narvidev/narvi/tools/lint/narvichecks/capabilityimportban"
)

// TestAnalyzer proves the analyzer fires on an import of any of the three
// banned packages planted in a shadow package (internal/app/shadowscm),
// in internal/app/sessionactor, and in internal/domain/review -- three
// real production packages this analyzer must keep off the capability
// registry entirely (docs/design/boundaries-design.md, section 1.4) -- and stays
// silent in every allowed location (controlplane, extension,
// internal/app/capability -- which legitimately imports license itself --
// and the two named httpapi gate files) and in a _test.go file (package
// "d").
//
// Every fixture package below lives under
// testdata/src/github.com/narvidev/narvi/... , mirroring this
// repository's own real import-path prefix exactly: Go's own internal-
// import rule (this whole design's own section-0 constraint) applies inside
// analysistest's synthetic GOPATH tree too, so a fixture importing
// .../internal/domain/license must itself be rooted at
// github.com/narvidev/narvi/... or the fixture fails to even COMPILE,
// before this analyzer ever runs.
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, capabilityimportban.Analyzer,
		"github.com/narvidev/narvi/internal/app/shadowscm",
		"github.com/narvidev/narvi/internal/app/sessionactor",
		"github.com/narvidev/narvi/internal/domain/review",
		"github.com/narvidev/narvi/controlplane",
		"github.com/narvidev/narvi/extension",
		"github.com/narvidev/narvi/internal/app/capability",
		"github.com/narvidev/narvi/internal/adapters/inbound/httpapi",
		"github.com/narvidev/narvi/d",
	)
}
