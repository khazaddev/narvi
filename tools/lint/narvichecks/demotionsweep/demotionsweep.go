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

const doc = `report calls to UpsertLiveEgressEnabled outside the demotion-aware packages

Flipping live_egress_enabled true->false must also run the demotion sweep
(technical plan §30.4): terminate every sandbox of the repo and cancel
in-flight push signals, because a write credential minted just before the
flip outlives it by the ScmCredentialTTL window. internal/app/seed
performs that pairing for a general upsert that can move in either
direction. internal/app/shadowoperator is also allowed: it backs Step
104's own "Activate" graduation gesture, which is a PROMOTION only
(false->true) -- its own doc comment states plainly that promotion "is
the safe direction on the sandbox side," since a shadow repo's sandboxes
have never held more than read-only, so it owes no sweep. That package's
own Activate function carries no boolean argument at all, so nothing in
it can call this method with false -- the promotion-only property is a
fact about its type, not a convention this analyzer merely trusts. A
THIRD new caller must go through one of these two, or run the sweep
itself and be added here deliberately -- this analyzer bans the call by
NAME, not by which direction a given caller happens to use it in, so
widening this list is exactly the deliberate act it exists to force.`

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
// store that DEFINES it; internal/app/seed, which pairs a true->false
// transition with repodemotion.Sweep; and internal/app/shadowoperator,
// whose own Activate is a promotion-only (false->true) caller that owes
// no sweep at all (§30.4: promotion "is the safe direction on the
// sandbox side") -- see this file's own doc comment for the full
// reasoning on why that third entry is safe to add. Extending this list
// further is the deliberate act this analyzer exists to force -- pair a
// new caller with a sweep first, or prove (and say, right here) why it
// does not need one.
var allowedDirs = []string{
	"/internal/adapters/outbound/postgres/",
	"/internal/app/seed/",
	"/internal/app/shadowoperator/",
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
