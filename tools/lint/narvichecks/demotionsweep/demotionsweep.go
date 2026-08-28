// Package demotionsweep implements a static-analysis check enforcing
// technical plan §30.4's demotion requirement structurally.
//
// §30 sets its own bar explicitly: a guarantee must be structural,
// something a future contributor cannot silently un-make, never
// per-call-site discipline. Demotion is the case where that bar is
// easiest to miss. Flipping repo_settings.live_egress_enabled from true
// to false does not, by itself, take any credential away: a write token
// minted just before the flip stays served for the ScmCredentialTTL
// window, and the underlying OAuth token never expires on that clock. So
// §30.4 requires demotion to terminate (or respawn) every sandbox of the
// repo and cancel in-flight push signals.
//
// That requirement lives in Go, not SQL -- matching a sandbox to a repo
// means parsing each session's own repos JSONB and its clone URLs, which
// the store method doing the flip cannot do inside its own statement. The
// sweep is therefore a separate call, and a separate call is exactly what
// a future contributor forgets: a REST demotion handler that calls the
// store directly would flip the flag, leave every sandbox running, and
// pass every existing test.
//
// This analyzer removes that possibility from the type of mistake it is.
// UpsertLiveEgressEnabled may be called only from the one package that
// also runs the sweep. Adding a second caller is a CI failure naming this
// requirement, not a silent regression discovered by a customer noticing
// a branch appear in their repository.
package demotionsweep

import (
	"go/ast"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const doc = `report calls to UpsertLiveEgressEnabled outside the demotion-aware package

Flipping live_egress_enabled true->false must also run the demotion sweep
(technical plan §30.4): terminate every sandbox of the repo and cancel
in-flight push signals, because a write credential minted just before the
flip outlives it by the ScmCredentialTTL window. Only internal/app/seed
performs that pairing today. A new caller must go through it, or run the
sweep itself and be added here deliberately.`

// Analyzer reports any call selecting UpsertLiveEgressEnabled from a file
// outside the packages allowed to make it.
var Analyzer = &analysis.Analyzer{
	Name: "demotionsweep",
	Doc:  doc,
	Run:  run,
}

// flipMethod is the store method that changes a repository's egress mode.
const flipMethod = "UpsertLiveEgressEnabled"

// allowedDirs are the packages permitted to call flipMethod: the postgres
// store that DEFINES it, and internal/app/seed, the one caller that pairs
// it with repodemotion.Sweep. Extending this list is the deliberate act
// this analyzer exists to force -- pair the call with a sweep first.
var allowedDirs = []string{
	"/internal/adapters/outbound/postgres/",
	"/internal/app/seed/",
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if skipFile(filename) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != flipMethod {
				return true
			}
			pass.Reportf(sel.Pos(),
				"%s changes a repository's egress mode and must be paired with the demotion sweep (§30.4): a write credential minted before the flip outlives it by ScmCredentialTTL, so a demotion that does not terminate the repo's sandboxes leaves them able to write",
				flipMethod)
			return true
		})
	}
	return nil, nil
}

// skipFile exempts tests and the allowed packages. Tests are exempt
// because a test that calls the store directly is not a production
// demotion path -- it asserts the store's own behavior, which is
// precisely what the seed's own sweep tests need to do.
func skipFile(filename string) bool {
	if strings.HasSuffix(filename, "_test.go") {
		return true
	}
	clean := filepath.ToSlash(filename)
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	for _, dir := range allowedDirs {
		if strings.Contains(clean, dir) {
			return true
		}
	}
	return false
}
