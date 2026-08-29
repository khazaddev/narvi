// Package httpclientban implements the second of §30.3's two CI arch-test
// mechanisms: a ban on constructing or invoking net/http's CLIENT-side
// surface outside the trees that legitimately perform egress.
//
// # Why this is not an import ban
//
// Banning the "net/http" import outright (execimportban's own shape for
// "os/exec") fails the repo on day one: technical plan §30.3 verified 58
// non-test files across 11 packages outside the allowed trees import
// net/http today, every one of them for the SERVER side
// (http.ResponseWriter, *http.Request, http.HandlerFunc, status/method
// constants) -- receiving and answering a request is not an egress
// capability, and an import-level ban cannot distinguish that from the
// client side it exists to forbid. So this analyzer names the exact
// symbols that let code reach OUT -- http.Client, http.DefaultClient,
// http.DefaultTransport, http.NewRequest, http.NewRequestWithContext,
// http.Get, http.Post, http.Head, http.Transport -- and leaves every
// other net/http identifier (http.ResponseWriter, *http.Request,
// http.HandlerFunc, http.StatusOK, http.MethodGet, ...) untouched,
// everywhere.
//
// # The composition-root exception, and why it is narrower than the
// fully allowed trees
//
// cmd/control-plane is this binary's own composition root: the one place
// a concrete *http.Client is legitimately constructed and wired into an
// outbound adapter's constructor (githubapp.New(http.DefaultClient, ...),
// chatgptoauth.New(http.DefaultClient, ...), and this Step's own
// slackapi.New/linearapi.New(http.DefaultClient, ...) fixes). It is NOT
// exempt from the INVOCATION half of the banned set: constructing a
// client there and handing it to a constructor is the composition root
// doing its job, but cmd/control-plane calling http.Get/http.Post/
// http.NewRequest(WithContext) directly would be the composition root
// issuing a live request itself, which is exactly the egress capability
// this whole arch-test exists to keep out of every place except the
// outbound adapters that are supposed to hold it. So cmd/control-plane
// gets construction symbols only; internal/adapters/outbound, internal/
// sandboxagent, and cmd/sandbox-agent get the full set, unconditionally.
//
// # The ratcheted baseline
//
// §30.3 audited TWO pre-existing call sites outside every allowed tree
// and required both pinned here rather than silently grandfathered. ONE
// remains, because this Step retired the other:
//
//  1. internal/adapters/inbound/auth/callback.go -- the OAuth sign-in
//     identity reads (fetchGitHubUser, fetchVerifiedPrimaryEmail,
//     checkOrgMembership, checkAnyOrgMembership). All GETs, never a
//     customer-repo write, no different in kind from the API GETs §30.1
//     already excludes from the suppression guarantee.
//
//  2. internal/adapters/inbound/slack/{ack.go,handler.go} -- GONE. §30.3
//     predicted this entry would "drop out of the baseline once this Step
//     ships", because the same Step's first compensating control moves
//     that construction behind an injected seam. It did: ack.go no longer
//     exists and handler.go constructs no client. The list below is the
//     evidence, not the intention.
//
// A file outside every allowed tree and not in that list fails on ANY
// banned symbol -- no partial credit, no directory-level exemption. A
// second entry, or a widened directory, requires editing this file, which
// is exactly the deliberate act this arch-test exists to force.
package httpclientban

import (
	"go/ast"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const doc = `report constructing or invoking net/http's client-side surface outside the outbound/sandbox trees

net/http.Client, .DefaultClient, .DefaultTransport, .NewRequest(WithContext),
.Get, .Post, .Head, and .Transport are this platform's own egress
capability (technical plan §30.3). Server-side net/http (ResponseWriter,
*Request, HandlerFunc, status/method constants) is unaffected and remains
permitted everywhere. Outside internal/adapters/outbound, internal/
sandboxagent, and cmd/sandbox-agent -- and, for construction only,
cmd/control-plane's own composition root -- any of these symbols is a new,
unreviewed egress path. A short, explicit, ratcheted baseline of
pre-existing audited call sites is defined in this package; anything else
must move behind one of this codebase's own gated outbound clients
instead of adding a new banned-symbol call site here.`

// Analyzer reports any construction or invocation of net/http's
// client-side symbols in a file outside the allowed trees/baseline.
var Analyzer = &analysis.Analyzer{
	Name: "httpclientban",
	Doc:  doc,
	Run:  run,
}

// bannedSymbols is net/http's own client-side surface (§30.3's own
// enumeration, verbatim). Referencing ANY of these outside an allowed
// tree or the baseline is a violation, whether as a call
// (http.Get(url)), a value (http.DefaultClient), or a type
// (http.Client{}, *http.Client) -- see this package's own doc comment for
// why a bare type reference counts too: a package accepting/holding a raw
// *http.Client is the exact "attractive nuisance" shape §30.2 already
// removed from the four outbound constructors' own nil-defaults, and
// leaving it un-bannable here would just move the nuisance one level up.
var bannedSymbols = map[string]bool{
	"Client":                true,
	"DefaultClient":         true,
	"DefaultTransport":      true,
	"NewRequest":            true,
	"NewRequestWithContext": true,
	"Get":                   true,
	"Post":                  true,
	"Head":                  true,
	"Transport":             true,
}

// invocationSymbols is the subset the composition root (cmd/control-plane)
// may NOT reference even though it is otherwise exempt -- see this
// package's own doc comment ("narrower than the fully allowed trees") for
// why construction is the composition root's job and invocation is not.
var invocationSymbols = map[string]bool{
	"NewRequest":            true,
	"NewRequestWithContext": true,
	"Get":                   true,
	"Post":                  true,
	"Head":                  true,
}

// allowedDirs get a full pass on every banned symbol -- the outbound
// adapters and the sandbox agent's own trees, exactly like
// execimportban's identically-named allowedDirs.
var allowedDirs = []string{
	"/internal/adapters/outbound/",
	"/internal/sandboxagent/",
	"/cmd/sandbox-agent/",
}

// compositionRootDir gets a NARROWER pass: construction symbols only, per
// this package's own doc comment.
const compositionRootDir = "/cmd/control-plane/"

// baseline is the ratcheted, EXACT set of files this arch-test's own
// day-one adversarial pass audited and pinned rather than silently
// grandfathered -- see this package's own doc comment for the "why" on
// each entry. Matched as a path SUFFIX (not a substring, unlike
// allowedDirs/compositionRootDir above): a baseline entry names one real
// file, never a directory, so a new file placed anywhere near it still
// fails closed.
var baseline = []string{
	"/internal/adapters/inbound/auth/callback.go",
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := filepath.ToSlash(pass.Fset.Position(file.Pos()).Filename)
		if !strings.HasPrefix(filename, "/") {
			filename = "/" + filename
		}
		if inAllowedDir(filename) || inBaseline(filename) {
			continue
		}
		compositionRoot := strings.Contains(filename, compositionRootDir)

		httpName, imported := httpImportName(file)
		if !imported {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != httpName {
				return true
			}
			symbol := sel.Sel.Name
			if !bannedSymbols[symbol] {
				return true
			}
			if compositionRoot && !invocationSymbols[symbol] {
				// Construction-shaped reference at the composition root:
				// allowed, see this package's own doc comment.
				return true
			}
			pass.Reportf(sel.Pos(), "net/http.%s is a client-side (egress) symbol (technical plan §30.3); it may be constructed or invoked only in internal/adapters/outbound, internal/sandboxagent, cmd/sandbox-agent, or (construction only) cmd/control-plane's own composition root -- server-side net/http (http.ResponseWriter, *http.Request, http.HandlerFunc, status/method constants) remains permitted everywhere", symbol)
			return true
		})
	}
	return nil, nil
}

// httpImportName reports the local identifier file uses for "net/http",
// and whether it imports it at all. A dot-import or blank import cannot
// be reliably matched by name and is treated as "not imported" -- no file
// in this codebase does either for net/http, and a future one that did
// would be its own, separate review problem.
func httpImportName(file *ast.File) (name string, ok bool) {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "net/http" {
			continue
		}
		if imp.Name == nil {
			return "http", true
		}
		if imp.Name.Name == "_" || imp.Name.Name == "." {
			return "", false
		}
		return imp.Name.Name, true
	}
	return "", false
}

// inAllowedDir mirrors execimportban's own identical directory-substring
// matching.
func inAllowedDir(filename string) bool {
	for _, dir := range allowedDirs {
		if strings.Contains(filename, dir) {
			return true
		}
	}
	return false
}

// inBaseline reports whether filename is one of this package's own
// explicit, pinned pre-existing call sites -- a SUFFIX match, so a
// baseline entry names one file, never a directory.
func inBaseline(filename string) bool {
	for _, f := range baseline {
		if strings.HasSuffix(filename, f) {
			return true
		}
	}
	return false
}
