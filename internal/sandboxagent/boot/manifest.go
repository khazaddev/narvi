package boot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// ImageManifestPath is the fixed, well-known location (§19.1) a shared
// warm-boot image bakes its own self-description manifest at:
// "/narvi/image-manifest.json". Baked ONLY by the build service at image
// build time (internal/adapters/outbound/modal's own BuildImage
// implementation, §19.1 point 4) -- a fresh/build/snapshot_restore boot
// never has one on disk (there was no image-build step to bake it), which
// is an entirely expected, non-error case (see LoadImageManifest's own
// doc comment).
const ImageManifestPath = "/narvi/image-manifest.json"

// ImageManifest is the plain data shape §19.1 bakes into every shared,
// warm-boot image: "{fingerprint, built_at, built_repo_shas: {name: sha}}".
// Read locally by sandbox-agent (LoadImageManifest) so it can decide the
// setup-rerun question (§19.4's workspaceMoved) with no extra
// control-plane round trip -- the image describes itself.
type ImageManifest struct {
	// Fingerprint is the domain/imagebuild.Fingerprint value this image was
	// built for -- carried for diagnostic/log purposes only; sandbox-agent
	// itself never recomputes or verifies it against anything (that
	// reconciliation, if any, belongs to whatever built and baked this
	// image, not to a boot-time reader of it).
	Fingerprint string `json:"fingerprint"`
	// BuiltAt is when the image build that produced this manifest
	// completed (image_builds.built_at's own value, §19.1) -- carried for
	// diagnostic/log purposes only, exactly like Fingerprint above:
	// sandbox-agent never reads it to make any decision. Populated only
	// when LoadImageManifest could actually interpret the manifest's raw
	// built_at value (see its own doc comment); left at the zero value
	// otherwise. Deliberately NOT decoded via json.Unmarshal's own
	// built-in time.Time support (RFC 3339 only): the build service that
	// bakes this file is an external, non-Go component (§19.1) with
	// nothing pinning its timestamp encoding to RFC 3339, and this field's
	// own diagnostic-only status must never be allowed to block decoding
	// BuiltRepoShas -- the one load-bearing field in the same document.
	BuiltAt time.Time `json:"-"`
	// BuiltRepoShas maps repo name to the exact commit SHA that repo was
	// checked out at when setup.sh last ran, at build time (image_builds.
	// built_repo_shas, §19.1/§19.2) -- §19.4's workspaceMoved computation
	// compares each repo's POST-SyncAll checked-out SHA against this map's
	// own entry for that repo's name.
	BuiltRepoShas map[string]string `json:"built_repo_shas"`
}

// manifestWire is LoadImageManifest's own decode target -- built_at is
// held as raw JSON rather than time.Time so that ANY value there (a
// string in some non-RFC-3339 encoding, a bare number, even a syntactically
// valid-but-uninterpretable token) is still syntactically valid JSON and
// therefore never fails this first decode pass. That first pass is what
// guarantees BuiltRepoShas -- the load-bearing field -- gets read
// regardless of what built_at looks like; only genuinely malformed JSON
// (the document itself doesn't parse) reaches LoadImageManifest's error
// return.
type manifestWire struct {
	Fingerprint   string            `json:"fingerprint"`
	BuiltAt       json.RawMessage   `json:"built_at"`
	BuiltRepoShas map[string]string `json:"built_repo_shas"`
}

// LoadImageManifest reads and parses path (ImageManifestPath in
// production; a test-only override in tests), returning:
//
//   - (manifest, true, nil) -- the manifest exists and parsed successfully.
//     This includes the case where built_at is present but not in any
//     timestamp encoding this reader understands (see parseBuiltAt): that
//     is logged (slog.Warn) and manifest.BuiltAt is left at its zero
//     value, but found is still true and err is still nil, because
//     BuiltRepoShas -- the only field anything downstream actually reads
//     (§19.4's workspaceMoved) -- decoded successfully. An unreadable
//     built_at must NEVER be conflated with "no manifest at all" below:
//     the log line above is exactly what lets an operator tell "the build
//     service is emitting a timestamp shape we don't understand" apart
//     from "there is no manifest";
//   - (ImageManifest{}, false, nil) -- the manifest does not exist at all,
//     an entirely expected, non-error outcome for any boot mode that never
//     had an image-build step bake one (fresh/build/snapshot_restore, or a
//     repo_image boot whose own image predates this Step -- see
//     ComputeWorkspaceMoved's own doc comment for why this case defaults
//     to "treat as moved");
//   - (ImageManifest{}, false, err) -- the path exists but a genuine I/O
//     failure (permission denied, ...) or the file is not even
//     syntactically valid JSON -- distinct from "does not exist" so a
//     caller can log the more alarming case (a present-but-broken
//     manifest) differently, even though BOTH non-nil-err and not-found
//     currently resolve to the exact same safe default (see
//     ComputeWorkspaceMoved). A merely-unexpected built_at encoding does
//     NOT fall into this branch -- see the first bullet above.
func LoadImageManifest(path string) (manifest ImageManifest, found bool, err error) {
	data, statErr := os.ReadFile(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return ImageManifest{}, false, nil
		}
		return ImageManifest{}, false, fmt.Errorf("boot: read image manifest %s: %w", path, statErr)
	}

	var wire manifestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return ImageManifest{}, false, fmt.Errorf("boot: parse image manifest %s: %w", path, err)
	}

	manifest = ImageManifest{
		Fingerprint:   wire.Fingerprint,
		BuiltRepoShas: wire.BuiltRepoShas,
	}
	if raw := bytes.TrimSpace(wire.BuiltAt); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if builtAt, ok := parseBuiltAt(raw); ok {
			manifest.BuiltAt = builtAt
		} else {
			// Diagnostic-only field, §19.1/ImageManifest.BuiltAt's own doc
			// comment -- log and continue rather than discarding a
			// manifest whose load-bearing BuiltRepoShas already decoded
			// fine above.
			slog.Warn("boot: image manifest built_at is not a recognized timestamp encoding; continuing without it (diagnostic-only field, does not affect workspaceMoved)",
				"path", path, "built_at_raw", string(raw))
		}
	}
	return manifest, true, nil
}

// builtAtStringLayouts are the additional string encodings parseBuiltAt
// tolerates for built_at beyond Go's own RFC 3339 parsing (time.RFC3339
// covers the "Z-suffixed" and "numeric offset" cases already; the extra
// layouts below cover the plausible near-misses an external, non-Go build
// service (§19.1) might emit -- a space instead of the "T" separator,
// with or without an explicit offset/zone).
var builtAtStringLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
}

// unixEpochMillisThreshold is the magnitude above which a bare numeric
// built_at is treated as milliseconds-since-epoch rather than
// seconds-since-epoch: a seconds value this large would land in the year
// ~33658, which is never a plausible build time, whereas it is an entirely
// ordinary millisecond timestamp (e.g. JavaScript's Date.now()). The same
// heuristic threshold is used by most JSON timestamp libraries that accept
// both encodings.
const unixEpochMillisThreshold = 1e12

// builtAtMinPlausibleUnixSeconds and builtAtMaxPlausibleUnixSeconds bound
// a bare numeric built_at -- interpreted in whichever unit (seconds or
// milliseconds) unixEpochMillisThreshold selects -- to a plausible
// calendar range for an image-build timestamp: [2000-01-01, 2200-01-01],
// a comfortably wide window around any imaginable operational lifetime of
// this format.
//
// This bound is not cosmetic: num >= unixEpochMillisThreshold only tells
// us which unit to interpret num as, not that num is small enough for
// int64(num) to be well-defined. A built_at far outside any plausible
// calendar range (e.g. 1e30 -- arbitrarily large garbage, or a
// build-service bug emitting nanoseconds instead of seconds/millis) would
// overflow the float64->int64 conversion time.Unix/time.UnixMilli need;
// Go's own spec leaves that conversion's result implementation-defined
// when the value doesn't fit, and on this toolchain it silently clamps to
// math.MaxInt64, producing a nonsense time.Time (~year 292278994) that
// would otherwise report ok=true instead of being logged as unparseable.
// Bounding num in float64 space, before it is ever converted to int64,
// closes that gap for both the too-large and too-negative directions.
var (
	builtAtMinPlausibleUnixSeconds = float64(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	builtAtMaxPlausibleUnixSeconds = float64(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
)

// parseBuiltAt interprets raw (a non-empty, non-"null" json.RawMessage --
// the caller has already trimmed and checked that) as a built_at value in
// any of the encodings this reader tolerates: RFC 3339 (with or without
// fractional seconds), a space-separated near-miss of RFC 3339, or a bare
// JSON number as Unix epoch seconds or milliseconds, bounded to a
// plausible calendar range (see builtAtMinPlausibleUnixSeconds above).
// Returns ok=false for anything else -- callers must treat that as
// "diagnostic field unreadable", never as a reason to reject the whole
// manifest.
func parseBuiltAt(raw json.RawMessage) (time.Time, bool) {
	if raw[0] != '"' {
		var num float64
		if err := json.Unmarshal(raw, &num); err != nil {
			return time.Time{}, false
		}
		if num >= unixEpochMillisThreshold || num <= -unixEpochMillisThreshold {
			// Millis-space: compare against the same plausible-seconds
			// bound scaled up, entirely in float64, before ever
			// converting to int64.
			if num < builtAtMinPlausibleUnixSeconds*1000 || num > builtAtMaxPlausibleUnixSeconds*1000 {
				return time.Time{}, false
			}
			return time.UnixMilli(int64(num)).UTC(), true
		}
		if num < builtAtMinPlausibleUnixSeconds || num > builtAtMaxPlausibleUnixSeconds {
			return time.Time{}, false
		}
		return time.Unix(int64(num), 0).UTC(), true
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, false
	}
	for _, layout := range builtAtStringLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ComputeWorkspaceMoved implements §19.4's own per-repo workspaceMoved
// predicate for every repo named in currentSHAs (the post-clone/-sync
// checked-out HEAD SHA per repo, boot.DiscoverRepoSHAs's own return
// shape): workspaceMoved is true when that repo's current SHA does not
// equal manifest.BuiltRepoShas[name] -- SHA equality is the *entire*
// cheap dependency-diff check §19.4 specifies, nothing finer-grained.
//
// # The missing/unreadable-manifest safe default
//
// manifestFound: false (LoadImageManifest found nothing on disk, OR found
// something it could not read/parse) makes EVERY repo report
// workspaceMoved: true, unconditionally -- resolved here, deliberately,
// against §19.4's own explicit framing ("never silently miss a
// dependency"): a repo_image boot with NO manifest at all (an image built
// before this Step shipped, or a build-service bug that failed to bake
// one) has no cheap way to prove its dependencies are still warm, so the
// safe assumption is "assume it moved" -- rerun setup.sh non-fatally
// (§19.4's own policy already makes this cheap: a rerun on an
// already-warm workspace is expected to be fast) rather than silently
// skip it and risk a session with missing dependencies, which is the
// single worst failure class this whole design exists to avoid (it
// surfaces later as a confusing agent/tool error, never as a boot error).
// The alternative -- defaulting to workspaceMoved: false on a missing
// manifest -- would silently reproduce exactly the failure mode §19.4
// exists to close, for the one case (no manifest at all) that has the
// LEAST information to justify skipping setup.sh.
//
// A repo present in currentSHAs but absent from manifest.BuiltRepoShas
// (found manifest, but this specific repo has no entry -- e.g. a repo
// added to the session after the image was built) is likewise
// workspaceMoved: true for the identical reason: no recorded built SHA
// means no basis to claim this repo's dependencies are warm.
func ComputeWorkspaceMoved(manifest ImageManifest, manifestFound bool, currentSHAs map[string]string) map[string]bool {
	moved := make(map[string]bool, len(currentSHAs))
	for name, sha := range currentSHAs {
		if !manifestFound {
			moved[name] = true
			continue
		}
		builtSHA, ok := manifest.BuiltRepoShas[name]
		moved[name] = !ok || builtSHA != sha
	}
	return moved
}
