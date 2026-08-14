package boot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// dependencyManifestFilenames is the closed, fixed set of per-ecosystem
// dependency-lockfile names §19.6's first bullet discovers at a repo's own
// ROOT directory (never recursively into subdirectories -- a monorepo with
// multiple independently-versioned lockfiles is out of scope for this
// Step's own cheap, whole-repo digest). This Go slice IS this codebase's
// own canonical specification of "the set discovered at build time" (§19.6:
// "package-lock.json, pnpm-lock.yaml, requirements.txt, go.sum, ...") --
// whatever bakes /narvi/image-manifest.json's own dependencyManifestDigests
// field (an external, unmodeled build service, exactly like
// ImageManifest.Fingerprint's own doc comment already establishes for the
// analogous baked-vs-recomputed split) is expected to discover the SAME
// set; see docs/environments.md for the contract stated in prose, next to
// the delta-script contract this same Step adds.
//
// Sorted alphabetically here purely for readability -- ComputeDependencyManifestDigest
// below sorts its own working copy again explicitly rather than relying on
// this literal's own declared order, so a future edit that appends a new
// name out of order can never silently produce a non-deterministic digest.
var dependencyManifestFilenames = []string{
	"Cargo.lock",
	"composer.lock",
	"Gemfile.lock",
	"go.sum",
	"package-lock.json",
	"Pipfile.lock",
	"pnpm-lock.yaml",
	"poetry.lock",
	"requirements.txt",
	"yarn.lock",
}

// ComputeDependencyManifestDigest implements §19.6's own digest: for each
// name in dependencyManifestFilenames (processed in a fixed, sorted order
// so the result never depends on filesystem iteration order) that exists at
// repoDir's own root, its content is SHA-256'd individually and folded,
// name-prefixed, into one outer SHA-256 -- so which lockfiles are PRESENT
// is baked into the digest exactly as much as their own content is: a repo
// that drops pnpm-lock.yaml entirely produces a different digest than one
// that keeps it with byte-identical remaining files, never a silent
// collision. A repo with ZERO recognized lockfiles present (no package
// manager in use at all) still produces a well-defined, deterministic
// digest (the SHA-256 of an empty input) -- not an error -- which correctly
// matches build-to-build for a repo that genuinely never had one.
//
// Returns a non-nil error ONLY for a genuine I/O failure reading a lockfile
// that DOES exist (e.g. permission denied) -- a simply-absent file
// (os.ErrNotExist) is the routine, expected case for most of this list on
// most repos and is never itself an error. Every caller of this function
// treats a non-nil error as §19.6's own "unreadable... digest... means
// ineligible" case (evaluateDependencySkip, below) -- conservative,
// never a silent skip.
func ComputeDependencyManifestDigest(repoDir string) (string, error) {
	names := make([]string, len(dependencyManifestFilenames))
	copy(names, dependencyManifestFilenames)
	sort.Strings(names)

	outer := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(repoDir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("boot: read dependency manifest %s: %w", name, err)
		}
		inner := sha256.Sum256(data)
		// hash.Hash.Write (via outer, a *sha256 digest) never returns a
		// non-nil error per its own documented contract -- explicitly
		// discarded rather than checked, matching the standard library's
		// own convention for writing into a hash.Hash.
		_, _ = fmt.Fprintf(outer, "%s\x00%x\n", name, inner)
	}
	return hex.EncodeToString(outer.Sum(nil)), nil
}

// DependencySkipOutcome is the digest tier's own per-repo verdict (§19.6
// first bullet): whether the baked dependency-manifest digest -- read from
// /narvi/image-manifest.json's own new dependencyManifestDigests field,
// beside built_repo_shas -- proves this repo's dependencies are unchanged
// since the image was built, and, when it does not, WHY not. Match and
// Ineligible are deliberately DIFFERENT outcomes even though both fall
// through to the tier below identically in behavior: Match's absence means
// either "the digests genuinely disagree" (Mismatch -- a clean, POSITIVE
// proof dependencies moved) or "there is nothing trustworthy to compare at
// all" (Ineligible -- an unreadable/absent/unrecognized baked digest, or a
// boot-side compute error) -- §19.6's own explicit instruction is that
// these two must never be conflated in behavior OR in an operator's own
// reading of the boot log.
type DependencySkipOutcome string

const (
	// DependencySkipIneligible reports that no baked digest for this repo
	// could be trusted (manifest missing entirely, this repo has no entry,
	// the baked value is not a recognized digest encoding, or the
	// boot-side recompute itself failed) -- §19.6's own "unreadable,
	// absent, or unrecognized... means ineligible, fall through... never a
	// silent skip".
	DependencySkipIneligible DependencySkipOutcome = "ineligible"
	// DependencySkipMatch reports that the baked and recomputed digests
	// agree -- setup.sh is skipped entirely, no delta script needed.
	DependencySkipMatch DependencySkipOutcome = "match"
	// DependencySkipMismatch reports that both digests were read/computed
	// successfully and they disagree -- a genuine, POSITIVE proof
	// dependencies moved (§19.6's own "a manifest-digest mismatch DOES
	// prove dependencies moved" -- unlike workspaceMoved's own SHA
	// inequality, which proves nothing about dependencies at all). Still
	// falls through to the delta/full tiers below, and still never fatal
	// on a subsequent install failure -- see EvaluateHook's own HookDelta
	// doc comment and §19.6's own "the reason changes... but the rule
	// still holds, on availability grounds" instruction. This proof does
	// NOT license skipping the delta tier -- delta eligibility is an
	// orthogonal question (has setup.sh ITSELF changed), evaluated
	// independently regardless of this outcome.
	DependencySkipMismatch DependencySkipOutcome = "mismatch"
)

// isRecognizedDigestHex reports whether s looks like a hex-encoded SHA-256
// digest (64 lowercase hex characters) -- §19.6's own "unrecognized digest"
// case for a baked value that IS present but is not, in fact, a digest this
// reader could ever have produced itself (a build-service bug, a
// differently-shaped placeholder, ...). ComputeDependencyManifestDigest's
// own output always satisfies this by construction, so this check is only
// ever meaningful against a BAKED value, never a freshly-recomputed one.
func isRecognizedDigestHex(s string) bool {
	if len(s) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// evaluateDependencySkip is the pure decision behind DependencySkipOutcome:
// given whether a manifest was found at all, this repo's own baked digest
// (bakedDigest, bakedOK -- bakedOK is false when this repo has no entry in
// the manifest's dependencyManifestDigests map at all, mirroring
// ComputeWorkspaceMoved's own "no entry -> no basis to claim warm" missing-
// key handling exactly), and this repo's freshly-recomputed digest
// (currentDigest, currentErr from ComputeDependencyManifestDigest above),
// returns the tier's own verdict. Every ineligible path is deliberately
// ordered before the comparison itself, per §19.6's own "unreadable,
// absent, or unrecognized... means ineligible" -- never a comparison
// against a value this function cannot actually trust.
func evaluateDependencySkip(manifestFound bool, bakedDigest string, bakedOK bool, currentDigest string, currentErr error) DependencySkipOutcome {
	if !manifestFound || !bakedOK || !isRecognizedDigestHex(bakedDigest) || currentErr != nil {
		return DependencySkipIneligible
	}
	if bakedDigest == currentDigest {
		return DependencySkipMatch
	}
	return DependencySkipMismatch
}

// setupUnchangedSinceBuild answers §19.6's third-bullet delta-script
// eligibility predicate EXACTLY as specified, no new hashing scheme: `git
// -C repoDir diff --quiet builtSHA HEAD -- setup.sh`. A clean (exit 0) diff
// means setup.sh is byte-identical between the built SHA and the checked-
// out HEAD -- returns (true, nil). A real diff (exit 1, git's own documented
// convention for `--quiet`) returns (false, nil): a clean, definitive "no",
// never an error. This SAME single predicate also correctly covers a branch
// that ADDED or REMOVED setup.sh entirely (§19.6: "no separate empty-case
// handling needed") -- git diff already treats a path's appearance/
// disappearance as a real diff on that path, exit 1, handled identically to
// any other content change.
//
// Any OTHER outcome (git itself missing, builtSHA not resolvable in this
// repo's own object store, a timeout, any exit code other than 0/1) is
// returned as a genuine error -- §19.6's own "any git error on this check
// is conservative: ineligible, fall through to full setup.sh".
//
// Run via a bare exec.CommandContext, NOT through the supervisor -- exactly
// DiscoverRepoSHAs/repoHeadSHA's own established precedent (fingerprint.go)
// for this identical class of operation: a very minor, sub-second, local-
// only git-plumbing call made at the same "collect boot facts" point in the
// sequence, before RunBoot's own supervised-process machinery is ever
// exercised.
func setupUnchangedSinceBuild(repoDir, builtSHA string, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "diff", "--quiet", builtSHA, "HEAD", "--", string(dependencyManifestSetupFilename))
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("boot: git diff --quiet %s HEAD -- setup.sh in %s: %w", builtSHA, repoDir, err)
}

// dependencyManifestSetupFilename names the one file setupUnchangedSinceBuild
// diffs -- a local alias for sandboxboot.HookSetup's own literal value
// ("setup.sh") kept as a plain string here rather than importing
// internal/domain/sandboxboot into this file: every other file in this
// package already imports that package for its own reasons (hooks.go,
// runboot.go), so this avoids adding a fourth redundant import purely for
// one string literal used by exactly one function.
const dependencyManifestSetupFilename = "setup.sh"

// SetupRerunLadder is one repo's own precomputed input to §19.6's graduated
// setup-rerun ladder -- both fields computed ONCE per boot (never per-hook)
// by ComputeSetupRerunLadder below, mirroring workspaceMoved's own "one
// manifest read covers every repo" shape exactly. Consulted by
// internal/sandboxagent/boot's own runSetupRerunLadder (hooks.go) for
// exactly the one cell it applies to: BootModeRepoImage's HookSetup, when
// workspaceMoved is already true.
type SetupRerunLadder struct {
	// DependencySkip is the digest tier's own verdict (§19.6 first bullet).
	DependencySkip DependencySkipOutcome
	// DeltaEligible is §19.6's third-bullet predicate: setup.sh itself is
	// provably unchanged since the built SHA (setupUnchangedSinceBuild
	// returned (true, nil)). This says nothing about whether sync.sh
	// actually EXISTS on disk -- that is checked at run time, exactly like
	// every other hook's own presence check (hookScriptPresent).
	DeltaEligible bool
}

// RerunReason is the closed, structured vocabulary §19.6's own instruction
// requires every ladder decision to log (§5.3): "skip / delta / full /
// ineligible-fallback". internal/sandboxagent/boot's own runSetupRerunLadder
// logs one line per tier actually consulted, each carrying exactly one of
// these four values as its own "outcome" attribute -- so a single repo's
// boot can produce more than one logged decision (e.g. "digest tier:
// ineligible-fallback" immediately followed by "delta tier: delta"), each
// individually auditable, not merely the final result.
type RerunReason string

const (
	// RerunReasonSkip reports that the digest tier proved dependencies
	// unchanged -- nothing runs.
	RerunReasonSkip RerunReason = "skip"
	// RerunReasonDelta reports that the delta script (sync.sh) is eligible
	// and present -- it runs instead of full setup.sh.
	RerunReasonDelta RerunReason = "delta"
	// RerunReasonFull reports that full setup.sh runs -- the ladder's own
	// floor, reached whenever neither tier above actually resolved the
	// decision (a clean digest mismatch, a delta script that is
	// ineligible/absent, or a delta script that ran and failed).
	RerunReasonFull RerunReason = "full"
	// RerunReasonIneligibleFallback reports that a TIER's own check could
	// not be trusted (no/unrecognized baked digest, a boot-side compute
	// error, a git error on the setup.sh-unchanged check) -- that tier
	// falls through to the next one down, conservatively, never a silent
	// skip.
	RerunReasonIneligibleFallback RerunReason = "ineligible-fallback"
)

// ComputeSetupRerunLadder computes §19.6's own two additional per-repo
// predicates -- beyond workspaceMoved itself -- for every repo currentSHAs
// names, unconditionally, regardless of mode/workspaceMoved: exactly
// ComputeWorkspaceMoved's own precedent (manifest.go), computed
// unconditionally at the same point in the boot sequence because
// EvaluateHook-driven callers only ever actually CONSULT these values for
// the one cell that matters (repo_image + HookSetup + workspaceMoved), so
// computing them uniformly keeps this call site as simple as
// ComputeWorkspaceMoved's own.
//
// A missing/unreadable manifest (manifestFound: false) makes every repo's
// own DependencySkip resolve to DependencySkipIneligible and DeltaEligible
// to false -- the identical safe default ComputeWorkspaceMoved documents
// for its own "assume moved" case, applied here as "assume neither
// cheaper tier is available, fall all the way through to full setup.sh" --
// never a silent skip, never a spuriously-preferred delta script.
func ComputeSetupRerunLadder(manifest ImageManifest, manifestFound bool, workspaceDir string, currentSHAs map[string]string, timeout time.Duration) map[string]SetupRerunLadder {
	ladder := make(map[string]SetupRerunLadder, len(currentSHAs))
	for name := range currentSHAs {
		repoDir := filepath.Join(workspaceDir, name)

		currentDigest, digestErr := ComputeDependencyManifestDigest(repoDir)
		bakedDigest, bakedOK := manifest.DependencyManifestDigests[name]
		skip := evaluateDependencySkip(manifestFound, bakedDigest, bakedOK, currentDigest, digestErr)

		var eligible bool
		if manifestFound {
			if builtSHA, ok := manifest.BuiltRepoShas[name]; ok {
				unchanged, err := setupUnchangedSinceBuild(repoDir, builtSHA, timeout)
				eligible = err == nil && unchanged
			}
		}

		ladder[name] = SetupRerunLadder{DependencySkip: skip, DeltaEligible: eligible}
	}
	return ladder
}
