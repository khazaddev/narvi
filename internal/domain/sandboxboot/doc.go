// Package sandboxboot implements the pure decision logic behind
// sandbox-agent's boot sequence (§6.4, "Sandbox boot contract"; §5.3
// observability):
//
//   - BootMode + ParseBootMode (bootmode.go): the four wire values
//     NARVI_BOOT_MODE carries (build | fresh | repo_image |
//     snapshot_restore). §6.4 gives no default boot mode, so an
//     unset/unrecognized value fails fast via *InvalidBootModeError rather
//     than silently falling back to one mode.
//   - Hook + HookOutcome + EvaluateHook (hook.go): §6.4's hook policy
//     verbatim -- "setup.sh runs only in fresh/build (fatal only in
//     build); start.sh runs in all non-build modes (primary repo fatal,
//     secondaries warn)" -- as an explicit decision function, not left to
//     inference at every call site. Amended by §19.4 (workspaceMoved)
//     and §19.6 (the optional HookDelta "sync.sh" script) -- see
//     hook.go's own doc comments for both.
//   - BootFingerprint (fingerprint.go): the plain data shape §5.3 requires
//     sandbox-agent log first, before any other line ("binary version,
//     image digest, repo SHAs, boot mode"). Assembly from live inputs
//     (env vars, best-effort git plumbing) is impure and lives in
//     internal/sandboxagent/boot instead.
//
// Every function here is pure per §11: no I/O, no time.Now(), no
// randomness, and (per §1's "Domain has zero external dependencies") no
// import of contracts/gen/go/* or anything outside the standard library --
// same self-contained convention as every other internal/domain/*
// package. In particular this package does NOT import
// contracts/gen/go/sessionconfig even though that generated package's
// SessionConfigBootMode enum carries the exact same four string values:
// only NARVI_BOOT_MODE's raw string is read this Step, never the full
// SESSION_CONFIG document, so there is no actual need to touch the
// contracts package here at all. A later Step that does parse the full
// document can reconcile the two representations then, by string value.
package sandboxboot
