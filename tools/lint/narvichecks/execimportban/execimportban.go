// Package execimportban implements the first of §30.3's two CI arch-test
// mechanisms: an import ban on "os/exec" outside the trees that
// legitimately spawn subprocesses.
//
// §30.3's own compensating controls for the synchronous-ingress-writes
// perimeter name a CI arch-test as the second of three required layers
// (the first being the single-gated-client-per-provider seam this
// package's siblings, internal/app/shadowslack and internal/app/
// shadowlinear, implement). os/exec is the plainest possible execution
// vector this platform's own egress guarantee assumes does not exist
// outside the sandbox/outbound trees -- a subprocess spawned from an
// ingress handler, a review pipeline, or any other in-process code could
// reach the network (or the filesystem, or a credential cache) by a path
// none of this design's other layers were built to see.
//
// This tree is verified genuinely clean today (0 files outside the
// allowed trees import "os/exec"), so unlike httpclientban this analyzer
// carries no baseline at all: any occurrence anywhere outside
// internal/adapters/outbound, internal/sandboxagent, or cmd/sandbox-agent
// is a CI failure, full stop.
package execimportban

import (
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const doc = `report "os/exec" imports outside the sandbox/outbound trees

os/exec is a subprocess-spawning capability. Technical plan §30.3 requires
it stay confined to internal/adapters/outbound, internal/sandboxagent, and
cmd/sandbox-agent -- the only trees whose own job is to run untrusted or
externally-facing processes. An import anywhere else is a new, unreviewed
execution vector outside every other layer this design's egress guarantee
depends on.`

// Analyzer reports any "os/exec" import in a file outside the allowed
// trees.
var Analyzer = &analysis.Analyzer{
	Name: "execimportban",
	Doc:  doc,
	Run:  run,
}

// bannedImportPath is the quoted import path as it appears in an
// *ast.ImportSpec's own Path.Value (still double-quoted -- ast.ImportSpec
// never unquotes it for you).
const bannedImportPath = "os/exec"

// allowedDirs are the trees permitted to import os/exec -- the sandbox
// provider's own outbound adapter, the sandbox agent's own internal
// packages, and the sandbox-agent binary itself. Matched as a path
// substring, exactly like tools/lint/narvichecks/demotionsweep's own
// allowedDirs, so this works identically against both the real repo tree
// and this package's own testdata/src layout.
var allowedDirs = []string{
	"/internal/adapters/outbound/",
	"/internal/sandboxagent/",
	"/cmd/sandbox-agent/",
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if inAllowedDir(filename) {
			continue
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || path != bannedImportPath {
				continue
			}
			pass.Reportf(imp.Pos(), "\"os/exec\" may only be imported from internal/adapters/outbound, internal/sandboxagent, or cmd/sandbox-agent (technical plan §30.3): a subprocess-spawning capability outside those trees is an execution vector this platform's own egress guarantee assumes does not exist")
		}
	}
	return nil, nil
}

// inAllowedDir mirrors demotionsweep.skipFile's own directory-substring
// matching exactly (see that function's own doc comment) -- deliberately
// duplicated rather than shared, matching this codebase's own established
// convention of small, per-analyzer copies over a forced shared helper
// package no Step has ever introduced for these checkers.
func inAllowedDir(filename string) bool {
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
