package gitclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/environment"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// CloneResult is one repo's outcome from CloneAll.
type CloneResult struct {
	Repo sessionconfig.SessionConfigReposElem
	// Primary is true for exactly the repo at position 0 in the manifest
	// (§3.4: "position 0 = primary").
	Primary bool
	// Dir is workspaceDir/<Repo.Name> -- set even when Err is non-nil, so
	// callers (WriteAgentsManifest, boot logging) know where this repo
	// was supposed to land, with exactly one exception: when Repo.Name/
	// Url/Branch itself fails reposource validation (see validateRepoSpec
	// below), Dir is left as the empty string rather than computed via
	// filepath.Join -- an invalid Name is precisely a potential path-
	// traversal payload, so no directory join is ever performed against
	// it at all, not even a "harmless" string-only one.
	Dir string
	// Err is non-nil if this repo's clone failed.
	Err error
}

// CloneAll clones every repo in repos, IN ORDER, into workspaceDir/<name>,
// via a real `git clone` subprocess spawned through sup (never a bare
// exec.Command) -- see doc.go for the full criticality/credential-helper/
// branch semantics. workspaceDir is created (MkdirAll) if it does not
// already exist, since `git clone` itself requires its target's PARENT
// directory to already exist.
//
// pathScope is the session's own Environment.PathScope (§14.1), when one
// is attached -- nil or empty means unscoped, the overwhelming common
// case, and produces ZERO behavior change: no sparse-checkout invocation
// at all. When non-empty, EVERY repo's clone (§3.4/§14.1: a session's
// Environment applies uniformly across its always-a-list repos, there is
// no per-repo scoping concept anywhere in this codebase) is immediately
// followed by `git sparse-checkout set --no-cone -- <patterns...>` --
// applySparseCheckout's own doc comment explains why --no-cone is
// required. pathScope is validated (internal/domain/environment.
// ValidatePathScope) ONCE, before any repo is even attempted: an invalid
// session-wide setting is a fatal configuration error for the whole spawn,
// not a single repo's problem.
//
// On a primary (repos[0]) clone (or, when scoped, sparse-checkout)
// failure, CloneAll returns immediately with a fatal error -- no repo
// after it is attempted, matching RunHooks' own "any fatal failure stops
// immediately" semantics exactly. A secondary repo's failure is logged as
// a warning and does not stop the loop; subsequent repos still get
// cloned. results always reflects every repo actually attempted (in
// order), regardless of outcome.
func CloneAll(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []sessionconfig.SessionConfigReposElem,
	pathScope []string,
	cloneTimeout, stopGrace time.Duration,
) ([]CloneResult, error) {
	if len(repos) == 0 {
		return nil, nil
	}

	if err := environment.ValidatePathScope(pathScope); err != nil {
		return nil, fmt.Errorf("gitclone: invalid path scope: %w", err)
	}

	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("gitclone: create workspace dir %s: %w", workspaceDir, err)
	}

	credHelperArg, err := CredHelperGitArg()
	if err != nil {
		return nil, fmt.Errorf("gitclone: determine credential helper: %w", err)
	}

	scoped := len(pathScope) > 0

	results := make([]CloneResult, 0, len(repos))
	for i, repo := range repos {
		primary := i == 0

		// Validate BEFORE any filepath.Join or sup.Spawn happens for this
		// repo -- repo.Name/Url/Branch are all session-controlled and
		// reach this loop with no upstream validation of their own (see
		// reposource's own package doc comment for the full argument-
		// injection/path-traversal reasoning this closes).
		var dir string
		cloneErr := validateRepoSpec(repo)
		if cloneErr == nil {
			dir = filepath.Join(workspaceDir, repo.Name)
			cloneErr = cloneOne(ctx, sup, credHelperArg, repo, dir, cloneTimeout, stopGrace)
		}
		if cloneErr == nil && scoped {
			cloneErr = applySparseCheckout(ctx, sup, dir, pathScope, cloneTimeout, stopGrace)
		}
		results = append(results, CloneResult{Repo: repo, Primary: primary, Dir: dir, Err: cloneErr})

		if cloneErr == nil {
			continue
		}

		if primary {
			return results, fmt.Errorf("gitclone: primary repo %q failed to clone (fatal): %w", repo.Name, cloneErr)
		}
		platform.Logger(ctx).Warn("gitclone: secondary repo failed to clone, continuing",
			"repo", repo.Name, "error", cloneErr)
	}

	return results, nil
}

// applySparseCheckout runs `git -C <dir> sparse-checkout set --no-cone --
// <patterns...>` for one already-cloned repo (§14.1: "domain/gitstate's
// clone step runs `git sparse-checkout set <globs>` per repo when
// path_scope is present"). Excluded paths never materialize on the
// sandbox filesystem afterward -- §14.1's own enforcement guarantee.
//
// --no-cone is required -- verified directly against real git behavior,
// not assumed (sync_test.go's own house style, applied here too): git's
// default cone mode REJECTS exactly the gitignore-style glob syntax
// internal/domain/environment.ValidatePathScope already validates (e.g. a
// leading "/" combined with a "*" wildcard, such as "/apps/web/*"),
// demanding directory-only patterns instead ("specify directories rather
// than patterns"); non-cone mode is the one that actually supports
// arbitrary path.Match-syntax globs. "--" ends option parsing for
// everything after it, mirroring cloneOne's own exact defense-in-depth
// convention: even an already-validated pattern should never be
// positionally ambiguous to git's own argument parser -- a pattern
// beginning with "-" passes ValidatePathScope's own glob-SYNTAX check
// (path.Match's own grammar does not forbid a leading "-"), so this is a
// real, not merely theoretical, defense-in-depth gap were "--" omitted.
func applySparseCheckout(ctx context.Context, sup *supervisor.Supervisor, dir string, patterns []string, timeout, stopGrace time.Duration) error {
	args := append([]string{"-C", dir, "sparse-checkout", "set", "--no-cone", "--"}, patterns...)

	proc, err := sup.Spawn(supervisor.Spec{Path: "git", Args: args})
	if err != nil {
		return fmt.Errorf("spawn git sparse-checkout set: %w", err)
	}

	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, waitErr := proc.Wait(stepCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return fmt.Errorf("git sparse-checkout set: did not complete within %s: %w", timeout, waitErr)
	}
	if result.Err != nil {
		return fmt.Errorf("git sparse-checkout set: %w", result.Err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("git sparse-checkout set: exited %d", result.ExitCode)
	}
	return nil
}

// validateRepoSpec runs every internal/domain/reposource validator this
// repo's fields need, in order, stopping at the first failure -- Branch
// is validated only when non-nil (§3.4: nil means "the repo's own
// default branch", reposource.ValidateBranch is never invoked for that
// case). Called BEFORE any filepath.Join or sup.Spawn happens for this
// repo (see CloneAll's own loop and reposource's own package doc comment
// for the argument-injection/path-traversal reasoning this closes).
func validateRepoSpec(repo sessionconfig.SessionConfigReposElem) error {
	if err := reposource.ValidateRepoName(repo.Name); err != nil {
		return fmt.Errorf("gitclone: invalid repo name: %w", err)
	}
	if err := reposource.ValidateRepoURL(repo.Url); err != nil {
		return fmt.Errorf("gitclone: invalid repo url: %w", err)
	}
	if repo.Branch != nil {
		if err := reposource.ValidateBranch(*repo.Branch); err != nil {
			return fmt.Errorf("gitclone: invalid repo branch: %w", err)
		}
	}
	return nil
}

// cloneOne spawns `git clone` for exactly one repo and waits for it,
// bounded by cloneTimeout -- mirroring internal/sandboxagent/boot's own
// runHook precisely: a hang is stopped (bounded by stopGrace, using the
// OUTER ctx for the Stop call, not the already-expired clone-scoped
// context) and reported as a timeout failure; a non-zero exit or a wait
// failure is likewise a real, returned error.
func cloneOne(
	ctx context.Context,
	sup *supervisor.Supervisor,
	credHelperArg string,
	repo sessionconfig.SessionConfigReposElem,
	dir string,
	cloneTimeout, stopGrace time.Duration,
) error {
	args := []string{"clone", "-c", "credential.helper=" + credHelperArg}
	if repo.Branch != nil {
		args = append(args, "--branch", *repo.Branch)
	}
	// "--" ends option parsing for everything after it (verified directly
	// against real `git clone` behavior, not assumed) -- defense in depth
	// alongside validateRepoSpec's own rejection above: even an already-
	// validated repo.Url/dir should never be positionally ambiguous to
	// git's own argument parser.
	args = append(args, "--", repo.Url, dir)

	proc, err := sup.Spawn(supervisor.Spec{
		Path: "git",
		Args: args,
		// Env is DELIBERATELY left at its zero value (nil, "inherit this
		// process's own environment") -- a reviewed choice, not an
		// oversight. git's own credential.helper mechanism (credHelperArg,
		// configured above via CredHelperGitArg) re-execs THIS SAME
		// sandbox-agent binary as `<binary> credential-helper get`, as
		// git's OWN child process, inheriting whatever env git itself
		// received here -- i.e. exactly what this Spec.Env carries,
		// nothing more. cmd/sandbox-agent's own runCredentialHelper calls
		// boot.Load(), which reads NARVI_SESSION_CONFIG via os.Getenv and
		// fails outright ("nothing to fetch credentials for") if it is
		// absent, so stripping it here would BREAK git authentication for
		// every private repo clone -- a real functional regression, not a
		// hardening win. A hand-built allowlist would also risk silently
		// omitting something the real `git` binary or its transport
		// (http/ssh) legitimately needs (PATH, HOME, an ssh-agent socket,
		// ...) that isn't yet enumerated anywhere in this codebase.
	})
	if err != nil {
		return fmt.Errorf("spawn git clone for %s: %w", repo.Name, err)
	}

	cloneCtx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	result, waitErr := proc.Wait(cloneCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return fmt.Errorf("git clone %s: did not complete within %s: %w", repo.Name, cloneTimeout, waitErr)
	}

	if result.Err != nil {
		return fmt.Errorf("git clone %s: %w", repo.Name, result.Err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("git clone %s: exited %d", repo.Name, result.ExitCode)
	}
	return nil
}

// CredHelperGitArg builds the exact `-c credential.helper=...` value: the
// CURRENTLY RUNNING binary's own absolute path (os.Executable()) plus the
// "credential-helper" subcommand, prefixed with `!` so git runs this exact
// shell command rather than treating it as a suffix appended to
// "git-credential-" (git itself appends the final "get"/"store"/"erase"
// argument when it invokes the helper). Exported (Step 21, "e2e happy
// path") so cmd/sandbox-agent's own HandlePush can configure the SAME
// per-invocation credential helper for `git push` that CloneAll already
// configures for `git clone` -- one shared implementation, two callers,
// never a second copy of this exact string-building logic.
func CredHelperGitArg() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return fmt.Sprintf("!'%s' credential-helper", exePath), nil
}
