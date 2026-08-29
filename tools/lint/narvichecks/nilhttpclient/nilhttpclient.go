// Package nilhttpclient implements a static-analysis check enforcing the
// consequence of technical plan §30.2's removal of the
// nil -> http.DefaultClient default from the outbound constructors.
//
// That removal was right, and it is why this check has to exist. Before
// it, New(nil, baseURL) produced a WORKING client that no egress layer
// could see -- an attractive nuisance §30.2 names and deletes. After it,
// the same call produces a client wired to a transport that refuses every
// request, so the omission is "useless rather than dangerous".
//
// Useless, and SILENT. Nothing in the type system, and nothing in any
// test, distinguishes a client that fails closed from a client that was
// never meant to run: a nil argument still compiles, still constructs,
// still satisfies every interface, and only stops working in production
// against the real API. Measured on this codebase, that is not
// hypothetical -- §30.2 changed the meaning of nil across four
// constructors and left THREE composition-root call sites passing it, so
// Slack notifications, Linear activities, and RWX preview dispatch were
// all dead while every check stayed green.
//
// The rule is therefore mechanical and general, not a list of the three
// that were found: passing a literal nil where a parameter is
// *net/http.Client is a CI failure. That covers constructors nobody has
// written yet, which is the half a fixed list would miss.
//
// Tests are exempt. A test that deliberately constructs a client which
// can make no request is exercising the fail-closed behavior, which is
// the behavior §30.2 wanted.
package nilhttpclient

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const doc = `report a literal nil passed where an *http.Client is expected

§30.2 removed the nil -> http.DefaultClient default from the outbound
constructors, so New(nil, ...) now builds a client whose transport refuses
every request. That is safe and silent: it compiles, constructs, and only
fails against the real API. Pass a real client.`

// Analyzer reports a nil literal in an argument position whose parameter
// type is *net/http.Client.
var Analyzer = &analysis.Analyzer{
	Name: "nilhttpclient",
	Doc:  doc,
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sig := calleeSignature(pass, call)
			if sig == nil {
				return true
			}
			for i, arg := range call.Args {
				if !isNilIdent(pass, arg) {
					continue
				}
				if !isHTTPClientParam(sig, i, call) {
					continue
				}
				pass.Reportf(arg.Pos(),
					"passing nil as the *http.Client builds a client whose transport refuses every request (§30.2 removed the http.DefaultClient default): pass the real client this surface must use")
			}
			return true
		})
	}
	return nil, nil
}

// calleeSignature resolves the called function's signature, or nil when
// the call is a conversion or otherwise not a function call.
func calleeSignature(pass *analysis.Pass, call *ast.CallExpr) *types.Signature {
	tv, ok := pass.TypesInfo.Types[call.Fun]
	if !ok {
		return nil
	}
	sig, _ := tv.Type.Underlying().(*types.Signature)
	return sig
}

// isNilIdent reports whether arg is the predeclared nil, resolved through
// the type checker rather than by name, so a local variable shadowing
// "nil" cannot produce a false positive.
func isNilIdent(pass *analysis.Pass, arg ast.Expr) bool {
	ident, ok := arg.(*ast.Ident)
	if !ok || ident.Name != "nil" {
		return false
	}
	obj := pass.TypesInfo.ObjectOf(ident)
	if obj == nil {
		return false
	}
	nilConst, ok := obj.(*types.Nil)
	return ok && nilConst != nil
}

// isHTTPClientParam reports whether parameter i of sig is *net/http.Client,
// accounting for variadic signatures.
func isHTTPClientParam(sig *types.Signature, i int, call *ast.CallExpr) bool {
	params := sig.Params()
	if params == nil || params.Len() == 0 {
		return false
	}
	idx := i
	if sig.Variadic() && i >= params.Len()-1 {
		if call.Ellipsis.IsValid() {
			return false
		}
		idx = params.Len() - 1
	}
	if idx >= params.Len() {
		return false
	}
	ptr, ok := params.At(idx).Type().(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Client" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "net/http"
}
