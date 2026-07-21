package imagebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
)

// Fingerprint deterministically names exactly one image-build target from
// (base, repoSHAs, runtimeVersion) -- §10 Phase 2: "fingerprint = repo SHAs
// + runtime version". Same inputs always produce the same output,
// REGARDLESS of repoSHAs' own map iteration order (Go deliberately
// randomizes it) -- repoSHAs' keys are sorted before hashing, so a
// multi-repo session's fingerprint is reproducible call after call, process
// after process. Different Base, different RepoSHAs (a changed SHA, an
// added/removed repo), or a different RuntimeVersion each produce a
// different fingerprint -- the whole point of fingerprinting on exactly
// these three inputs is that ANY of them changing means a different image
// is required.
//
// A plain SHA-256 hex digest (not cryptographically sensitive -- this is a
// content-addressed cache key, not a secret) over a small, unambiguous
// canonical encoding: base, then runtimeVersion, then each (name, sha) pair
// in sorted-by-name order, every field NUL-separated from the next so that,
// e.g., base="a"+runtimeVersion="bc" can never collide with
// base="ab"+runtimeVersion="c" (a plain concatenation without separators
// would allow exactly that class of collision).
func Fingerprint(base string, repoSHAs map[string]string, runtimeVersion string) string {
	names := make([]string, 0, len(repoSHAs))
	for name := range repoSHAs {
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	writeField(h, base)
	writeField(h, runtimeVersion)
	for _, name := range names {
		writeField(h, name)
		writeField(h, repoSHAs[name])
	}

	return hex.EncodeToString(h.Sum(nil))
}

// writeField writes s followed by a single NUL separator byte into w. A
// hash.Hash's Write never returns an error (documented behavior of every
// standard-library hash implementation, crypto/sha256 included), so the
// error return is deliberately ignored rather than threaded through every
// caller for a case that cannot occur.
func writeField(w io.Writer, s string) {
	_, _ = w.Write([]byte(s))
	_, _ = w.Write([]byte{0})
}
