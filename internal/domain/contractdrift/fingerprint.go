package contractdrift

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
)

// Fingerprint deterministically hashes entries -- a path -> git-blob-or-
// tree-sha map naming the immediate contents of a repo's configured
// contracts directory at one ref (§14.3). GitHub's Contents API gives each
// entry (file OR subdirectory) its own "sha" field, and a subdirectory's
// sha is ALREADY the recursive git tree hash of everything nested under
// it -- so entries is always a single, non-recursive directory listing;
// callers never need to recurse into a subdirectory entry themselves to
// fingerprint its contents (internal/adapters/outbound/githubapi's own
// ResolveContractsFingerprint is the one real caller, and its own doc
// comment/tests prove exactly this).
//
// Same inputs always produce the same output, REGARDLESS of entries' own
// map iteration order (Go deliberately randomizes it) -- entries' keys are
// sorted before hashing, mirroring internal/domain/imagebuild.Fingerprint's
// own identical precedent exactly (same writeField-style NUL-separated
// idiom, duplicated here rather than shared: this codebase has no common
// generic fingerprint helper today, and this small idiom is cheap enough
// to duplicate locally rather than force a premature shared package into
// existence for it).
//
// An empty (or nil) entries map is valid input and produces a fixed,
// deterministic digest -- this represents "the contracts directory exists
// at this ref but is empty", which this function deliberately treats as
// distinct from the caller-level "" sentinel meaning "no contracts
// directory exists at this ref at all" (Snapshot.ContractsFingerprint's
// own doc comment, drift.go): that distinction is a caller concern
// (exists vs. not), not something Fingerprint itself needs to encode --
// Fingerprint only ever hashes a listing it was actually given.
func Fingerprint(entries map[string]string) string {
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		writeField(h, p)
		writeField(h, entries[p])
	}

	return hex.EncodeToString(h.Sum(nil))
}

// writeField writes s followed by a single NUL separator byte into w --
// the exact same idiom internal/domain/imagebuild/fingerprint.go's own
// writeField uses, duplicated here (see this file's own doc comment for
// why). A hash.Hash's Write never returns an error (documented behavior
// of every standard-library hash implementation, crypto/sha256 included),
// so the error return is deliberately ignored rather than threaded
// through every caller for a case that cannot occur.
func writeField(w io.Writer, s string) {
	_, _ = w.Write([]byte(s))
	_, _ = w.Write([]byte{0})
}
