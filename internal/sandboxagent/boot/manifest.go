package boot

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// completed (image_builds.built_at's own value, §19.1).
	BuiltAt time.Time `json:"built_at"`
	// BuiltRepoShas maps repo name to the exact commit SHA that repo was
	// checked out at when setup.sh last ran, at build time (image_builds.
	// built_repo_shas, §19.1/§19.2) -- §19.4's workspaceMoved computation
	// compares each repo's POST-SyncAll checked-out SHA against this map's
	// own entry for that repo's name.
	BuiltRepoShas map[string]string `json:"built_repo_shas"`
}

// LoadImageManifest reads and parses path (ImageManifestPath in
// production; a test-only override in tests), returning:
//
//   - (manifest, true, nil) -- the manifest exists and parsed successfully;
//   - (ImageManifest{}, false, nil) -- the manifest does not exist at all,
//     an entirely expected, non-error outcome for any boot mode that never
//     had an image-build step bake one (fresh/build/snapshot_restore, or a
//     repo_image boot whose own image predates this Step -- see
//     ComputeWorkspaceMoved's own doc comment for why this case defaults
//     to "treat as moved");
//   - (ImageManifest{}, false, err) -- the path exists but a genuine I/O
//     failure (permission denied, ...) or JSON-decode failure prevented
//     reading it -- distinct from "does not exist" so a caller can log the
//     more alarming case (a present-but-broken manifest) differently, even
//     though BOTH non-nil-err and not-found currently resolve to the exact
//     same safe default (see ComputeWorkspaceMoved).
func LoadImageManifest(path string) (manifest ImageManifest, found bool, err error) {
	data, statErr := os.ReadFile(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return ImageManifest{}, false, nil
		}
		return ImageManifest{}, false, fmt.Errorf("boot: read image manifest %s: %w", path, statErr)
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return ImageManifest{}, false, fmt.Errorf("boot: parse image manifest %s: %w", path, err)
	}
	return manifest, true, nil
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
