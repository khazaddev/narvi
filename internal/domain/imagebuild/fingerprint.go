package imagebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"sort"
	"strings"
)

// Fingerprint deterministically names exactly one image-build target from
// (base, repos, runtimeVersion) -- §19.1's redefinition of §10 Phase 2's
// original "fingerprint = repo SHAs + runtime version": repos now maps
// repo name to that repo's normalized clone URL, NOT a resolved SHA. This
// re-keys an image on scope/SHA-independent inputs -- one shared image per
// distinct repo SET, refreshed continuously from each repo's default-branch
// tip (§19.2), rather than one image per exact SHA combination (which made
// a warm cache hit structurally rare: any push to any repo in the set
// invalidated the old key). Same inputs always produce the same output,
// REGARDLESS of repos' own map iteration order (Go deliberately randomizes
// it) -- repos' keys are sorted before hashing, so a multi-repo session's
// fingerprint is reproducible call after call, process after process.
// Different Base, different repos (a changed/differently-spelled-but-
// equivalent URL normalizes away, see NormalizeRepoURL below; a genuinely
// different remote, or an added/removed repo, does not), or a different
// RuntimeVersion each produce a different fingerprint -- the whole point
// of fingerprinting on exactly these three inputs is that ANY of them
// changing means a different image is required.
//
// image_builds carries no data from before this redefinition (the table
// is a pure cache, never a system of record) -- so, per §19.1's own
// explicit instruction, this redefines the function outright: no version
// tag on the digest, no dual-scheme migration period, existing rows are
// simply dropped by the migration that adds the columns this Step needs
// (migrations/000039_image_builds_shared_fingerprint.up.sql). This is a
// reusable technique, not a precedent to repeat casually: the NEXT time
// this fingerprint's inputs change, after real image_builds rows exist
// that must keep resolving during a rollout, a version-tagged digest and a
// dual-scheme window are the right tool -- just not needed here.
//
// A plain SHA-256 hex digest (not cryptographically sensitive -- this is a
// content-addressed cache key, not a secret) over a small, unambiguous
// canonical encoding: base, then runtimeVersion, then each (name, url)
// pair in sorted-by-name order, every field NUL-separated from the next so
// that, e.g., base="a"+runtimeVersion="bc" can never collide with
// base="ab"+runtimeVersion="c" (a plain concatenation without separators
// would allow exactly that class of collision).
func Fingerprint(base string, repos map[string]string, runtimeVersion string) string {
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	writeField(h, base)
	writeField(h, runtimeVersion)
	for _, name := range names {
		writeField(h, name)
		writeField(h, NormalizeRepoURL(repos[name]))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// NormalizeRepoURL canonicalizes a repo clone URL before it is hashed into
// a Fingerprint, so two differently-spelled URLs naming the SAME remote
// produce the SAME fingerprint key rather than silently minting two
// distinct images for one repo set (§19.1's own note: "reposource.
// ValidateRepoURL validates syntax today but does not canonicalize; the
// fingerprint's own key-derivation step must normalize before hashing, or
// two differently-spelled URLs for the same remote will silently produce
// two images"). internal/domain/reposource confirms the gap this closes:
// ValidateRepoURL only checks scheme=="https" and a non-empty Host, it
// never canonicalizes.
//
// Handles exactly the real-world GitHub clone-URL variance this design
// needs to not be fooled by, and nothing more:
//   - host case ("https://GitHub.com/x/y" vs "https://github.com/x/y") --
//     DNS hostnames are case-insensitive, but a naive string compare is
//     not, so the host is lower-cased;
//   - a trailing slash ("https://github.com/x/y/") -- trimmed;
//   - a trailing ".git" suffix ("https://github.com/x/y.git") -- trimmed,
//     since both forms clone the identical remote;
//   - any combination/repetition of the above (e.g. a trailing slash
//     AFTER the .git suffix) -- trimmed in a fixed-point-free two-pass
//     order (slash, then .git, then slash again) rather than assuming
//     only one ever appears.
//
// Deliberately does NOT attempt anything beyond this (query strings,
// userinfo, path casing, scheme normalization): reposource.ValidateRepoURL
// already restricts a repo URL to scheme=="https" with a non-empty host
// before it ever reaches this function, and GitHub repo paths ARE
// case-sensitive (unlike the host), so lower-casing the whole URL would be
// wrong, not merely unnecessary. A raw, unparseable string (should not
// happen past validation, but this function must never panic on one) is
// returned trimmed-but-otherwise-verbatim -- never causes an error, since
// Fingerprint has no error return to give one through.
func NormalizeRepoURL(rawURL string) string {
	s := strings.TrimSuffix(rawURL, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	parsed, err := url.Parse(s)
	if err != nil || parsed.Host == "" {
		// Not a parseable absolute URL (or no host) -- return the
		// trailing-slash/.git-trimmed string as-is rather than erroring;
		// reposource.ValidateRepoURL is the real gate for well-formedness,
		// this function only normalizes what it can.
		return s
	}
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
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
