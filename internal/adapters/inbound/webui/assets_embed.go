//go:build web_assets

package webui

import (
	"embed"
	"io/fs"
)

// embeddedDist is the web UI's real, built static bundle -- present only
// under the `web_assets` build tag (`go build -tags web_assets`, which
// requires `make web-build` to have already populated this file's own
// sibling dist/ directory, or this fails to compile with go:embed's own
// "pattern matches no files" error; see doc.go's own top comment for why
// the default, no-tag build never takes this path at all). dist/ here is
// NOT web/dist -- web/vite.config.ts's own outDir points its build output
// directly at this directory (go:embed's directive syntax forbids the `..`
// upward-traversal a plain web/dist target would need). `all:` is used
// (rather than plain `dist`) so a build that ever emits a dotfile under
// dist/ (Vite's own manifest output, if ever enabled) is embedded too, not
// silently dropped by go:embed's default "skip dot/underscore-prefixed
// names" rule.
//
//go:embed all:dist
var embeddedDist embed.FS

// DistFS is embeddedDist with its "dist" path prefix stripped, so
// DistFS.Open("index.html") -- exactly what Mount (mount.go) expects from
// either build variant -- works the same way it will once contributed by
// assets_stub.go's own nil value is swapped out.
var DistFS fs.FS = must(fs.Sub(embeddedDist, "dist"))

func must(sub fs.FS, err error) fs.FS {
	if err != nil {
		// fs.Sub only errors on a malformed dir argument ("dist" is a
		// fixed, valid literal above) -- unreachable in practice, panic
		// rather than thread an error through every caller of a package-
		// level var for a case that can only mean a bug in this file.
		panic("webui: fs.Sub(embeddedDist, \"dist\"): " + err.Error())
	}
	return sub
}
