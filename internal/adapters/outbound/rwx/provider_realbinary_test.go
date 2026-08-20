package rwx

import "testing"

// TestRWXAdapter_RealBinaryContractTest is this package's own one,
// deliberately-skipped real-binary contract test — mirroring the
// STRUCTURE of internal/adapters/outbound/opencode's own real-binary
// contract tests (e.g. sentinelfixagent_realbinary_test.go,
// TestSentinelFixAgent_RegistersAgainstRealPinnedBinary: start the real
// pinned binary, exercise it, assert the real response shape) — without
// pretending to actually run one.
//
// §4.1.1 calls for "CI contract tests against the real pinned binary,"
// the exact discipline already applied to OpenCode (§7). Unlike OpenCode
// — free, self-hostable, already installed in every environment this
// codebase's own CI and dev containers run in — RWX is a paid cloud SaaS
// product this environment has no account for: no `rwx` binary is on
// PATH and no RWX_ACCESS_TOKEN is configured anywhere reachable from this
// repo's own tests or CI (verified directly during this Step's own
// implementation: `which rwx` finds nothing, no RWX_* env var exists).
// This is a deliberate, named, user-approved scope decision,
// not a defect — see this feature's own landing PR description for the full
// "what's built vs. what's deferred" accounting.
//
// Every OTHER test in this package (provider_test.go, errors_test.go,
// runner_test.go, dispatchclient_test.go, previewnotifier_test.go)
// exercises this adapter's real logic — argument/env construction, error
// classification, capability values, the real Dispatches API's own wire
// shape — against a fake cliRunner or a fake httptest.Server, exactly
// mirroring how internal/adapters/outbound/modal's own tests exercise
// Modal's adapter with no real Modal account reachable either (modal/
// doc.go's own identical disclaimer). This ONE test function names,
// precisely, what a real pinned `rwx` CLI + RWX_ACCESS_TOKEN would let it
// additionally verify — the drift guard §4.1.3 calls for ("the
// pinned-CLI contract tests are the drift guard") — once both are
// provisioned in CI:
//
//  1. `rwx sandbox start --format json --config <path> --inactivity-timeout
//     <duration>` against a real, disposable RWX sandbox actually
//     succeeds, and its OWN JSON stdout/exit-code shape matches what
//     provider.go/wire.go assume (no "sandbox_id"-shaped field is parsed
//     today — identity is this adapter's own generated config path,
//     wire.go's own sandboxIdentityPath — this test would confirm RWX
//     never needs one).
//  2. The REAL exit-code/`--format json` error-envelope taxonomy for at
//     least one deliberately-triggered failure (an invalid config path, a
//     bad access token) — today errors.go's own classifyCLIError treats
//     EVERY nonzero exit as transient (§4.1.3: "unknown -> transient")
//     precisely because no real exit code has ever been observed; this
//     test is where the first real, permanent-vs-transient verdict gets
//     pinned.
//  3. `rwx sandbox stop` then `rwx sandbox start` against the SAME
//     identity actually preserves (or does not preserve) working-tree
//     state — §4.1's own FIRST exit criterion (§4.1.1/§4.1.3), settled
//     empirically, deciding whether Capabilities().Resume ever flips to
//     true (see Provider.Capabilities' own doc comment).
//  4. `rwx sandbox list --format json` actually reports org-wide truth
//     (§4.1's reconciliation/GC need) rather than only sandboxes visible
//     to the calling device/user (§4.1.3's own named, unverified gap).
//  5. Whether the `rwx` CLI subprocess actually honors inherited
//     HTTPS_PROXY (§4.1.1/§4.1.3's own named, unverified gap) — provable
//     only against a real egress proxy sitting in front of a real RWX
//     account.
func TestRWXAdapter_RealBinaryContractTest(t *testing.T) {
	t.Skip("no real rwx CLI binary or RWX_ACCESS_TOKEN is available in this environment/CI yet " +
		"(deliberate, user-approved §4.1 scope decision — see this file's own top comment for exactly " +
		"what this test would verify: real start/stop/list JSON+exit-code shapes, the real CLI error " +
		"taxonomy, empirical stop→start state-preservation for the Resume capability flag, List's real " +
		"org-wide scope, and whether the CLI subprocess actually honors HTTPS_PROXY). Provision a pinned " +
		"`rwx` binary on PATH plus RWX_ACCESS_TOKEN in CI, then replace this Skip with the real assertions " +
		"named above.")
}
