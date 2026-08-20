//go:build !web_assets

package webui

import "io/fs"

// DistFS is nil under the default (no `web_assets` tag) build -- see
// doc.go's own top comment for why: this variant carries no go:embed
// directive at all, so it needs no on-disk dist/ directory to compile,
// which is exactly the property that lets a fresh checkout that has never
// run `make web-build` still `go build ./...` successfully. Mount (mount.go)
// treats a nil assets fs.FS as a first-class, documented state -- "the
// frontend has not been built" -- and serves a small in-Go placeholder
// message for every SPA route instead of a missing file.
var DistFS fs.FS
