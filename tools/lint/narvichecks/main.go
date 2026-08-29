// Command narvichecks runs Narvi's project-specific static-analysis checks
// (nakedgoroutine, notimeliteral, demotionsweep, execimportban,
// httpclientban) as a golang.org/x/tools/go/analysis multichecker.
// Usage: go run ./tools/lint/narvichecks ./...
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/khazaddev/narvi/tools/lint/narvichecks/demotionsweep"
	"github.com/khazaddev/narvi/tools/lint/narvichecks/execimportban"
	"github.com/khazaddev/narvi/tools/lint/narvichecks/httpclientban"
	"github.com/khazaddev/narvi/tools/lint/narvichecks/nakedgoroutine"
	"github.com/khazaddev/narvi/tools/lint/narvichecks/nilhttpclient"
	"github.com/khazaddev/narvi/tools/lint/narvichecks/notimeliteral"
)

func main() {
	multichecker.Main(
		demotionsweep.Analyzer,
		execimportban.Analyzer,
		httpclientban.Analyzer,
		nakedgoroutine.Analyzer,
		nilhttpclient.Analyzer,
		notimeliteral.Analyzer,
	)
}
