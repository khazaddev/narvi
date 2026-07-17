// Command narvichecks runs Narvi's project-specific static-analysis checks
// (nakedgoroutine, notimeliteral) as a golang.org/x/tools/go/analysis
// multichecker. Usage: go run ./tools/lint/narvichecks ./...
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/khazaddev/narvi/tools/lint/narvichecks/nakedgoroutine"
	"github.com/khazaddev/narvi/tools/lint/narvichecks/notimeliteral"
)

func main() {
	multichecker.Main(
		nakedgoroutine.Analyzer,
		notimeliteral.Analyzer,
	)
}
