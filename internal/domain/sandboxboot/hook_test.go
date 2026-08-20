package sandboxboot_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
)

// TestEvaluateHook_TruthTable enumerates every row of the Step 13 truth
// table (§6.4's hook policy applied to all 4 modes x 2 hooks x 2 primary
// values -- 16 rows), EVERY ONE pinned with workspaceMoved: false so this
// table alone proves the (§19.4) amendment changed NOTHING for any
// pre-existing cell: every wantShouldRun/wantFatal value below is
// byte-identical to what EvaluateHook returned before that amendment
// existed. The one cell the amendment actually changes (repo_image +
// HookSetup, for BOTH primary values) gets its own dedicated
// workspaceMoved-true row directly below this table, in
// TestEvaluateHook_RepoImageSetup_WorkspaceMovedAmendment -- kept
// separate rather than folded into this table so this table's own
// "16 rows, all pinned false" invariant stays simple and self-checking.
func TestEvaluateHook_TruthTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode          sandboxboot.BootMode
		hook          sandboxboot.Hook
		primary       bool
		wantShouldRun bool
		wantFatal     bool
	}{
		{sandboxboot.BootModeBuild, sandboxboot.HookSetup, true, true, true},
		{sandboxboot.BootModeBuild, sandboxboot.HookSetup, false, true, true},
		{sandboxboot.BootModeBuild, sandboxboot.HookStart, true, false, false},
		{sandboxboot.BootModeBuild, sandboxboot.HookStart, false, false, false},

		{sandboxboot.BootModeFresh, sandboxboot.HookSetup, true, true, false},
		{sandboxboot.BootModeFresh, sandboxboot.HookSetup, false, true, false},
		{sandboxboot.BootModeFresh, sandboxboot.HookStart, true, true, true},
		{sandboxboot.BootModeFresh, sandboxboot.HookStart, false, true, false},

		{sandboxboot.BootModeRepoImage, sandboxboot.HookSetup, true, false, false},
		{sandboxboot.BootModeRepoImage, sandboxboot.HookSetup, false, false, false},
		{sandboxboot.BootModeRepoImage, sandboxboot.HookStart, true, true, true},
		{sandboxboot.BootModeRepoImage, sandboxboot.HookStart, false, true, false},

		{sandboxboot.BootModeSnapshotRestore, sandboxboot.HookSetup, true, false, false},
		{sandboxboot.BootModeSnapshotRestore, sandboxboot.HookSetup, false, false, false},
		{sandboxboot.BootModeSnapshotRestore, sandboxboot.HookStart, true, true, true},
		{sandboxboot.BootModeSnapshotRestore, sandboxboot.HookStart, false, true, false},
	}

	if len(tests) != 16 {
		t.Fatalf("test table has %d rows, want exactly 16 (4 modes x 2 hooks x 2 primary values)", len(tests))
	}

	for _, tc := range tests {
		name := string(tc.mode) + "/" + string(tc.hook) + "/primary=" + boolString(tc.primary)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := sandboxboot.EvaluateHook(tc.mode, tc.hook, tc.primary, false)
			if got.ShouldRun != tc.wantShouldRun {
				t.Errorf("EvaluateHook(%s, %s, primary=%v, workspaceMoved=false).ShouldRun = %v, want %v",
					tc.mode, tc.hook, tc.primary, got.ShouldRun, tc.wantShouldRun)
			}
			if got.FatalOnFailure != tc.wantFatal {
				t.Errorf("EvaluateHook(%s, %s, primary=%v, workspaceMoved=false).FatalOnFailure = %v, want %v",
					tc.mode, tc.hook, tc.primary, got.FatalOnFailure, tc.wantFatal)
			}
		})
	}
}

// TestEvaluateHook_RepoImageSetup_WorkspaceMovedAmendment proves the ONE
// cell §19.4's amendment actually changes (Step 42, breaking change): for
// BootModeRepoImage + HookSetup, workspaceMoved: true flips ShouldRun to
// true while FatalOnFailure stays false regardless (a moved workspace
// proves nothing about dependencies, so it can never justify failing the
// boot) -- for BOTH primary values, since setup.sh's own policy never
// depended on primary before this amendment either. Every OTHER (mode,
// hook) combination is proven completely unaffected by workspaceMoved by
// TestEvaluateHook_WorkspaceMovedIgnoredEverywhereElse below.
func TestEvaluateHook_RepoImageSetup_WorkspaceMovedAmendment(t *testing.T) {
	t.Parallel()

	for _, primary := range []bool{true, false} {
		primary := primary
		t.Run("primary="+boolString(primary), func(t *testing.T) {
			t.Parallel()

			got := sandboxboot.EvaluateHook(sandboxboot.BootModeRepoImage, sandboxboot.HookSetup, primary, true)
			want := sandboxboot.HookOutcome{ShouldRun: true, FatalOnFailure: false}
			if got != want {
				t.Errorf("EvaluateHook(repo_image, setup.sh, primary=%v, workspaceMoved=true) = %+v, want %+v",
					primary, got, want)
			}
		})
	}
}

// TestEvaluateHook_WorkspaceMovedIgnoredEverywhereElse proves workspaceMoved
// has ZERO effect outside the one cell above: setup.sh in every mode other
// than repo_image, and start.sh in every mode, produce the IDENTICAL
// HookOutcome regardless of workspaceMoved's value -- this is the "every
// OTHER existing (mode, hook, primary) combination's outcome must be
// byte-identical" guarantee, proven directly by construction (not just by
// pinning workspaceMoved: false in the main truth table above).
func TestEvaluateHook_WorkspaceMovedIgnoredEverywhereElse(t *testing.T) {
	t.Parallel()

	modes := []sandboxboot.BootMode{
		sandboxboot.BootModeBuild, sandboxboot.BootModeFresh,
		sandboxboot.BootModeRepoImage, sandboxboot.BootModeSnapshotRestore,
	}
	hooks := []sandboxboot.Hook{sandboxboot.HookSetup, sandboxboot.HookStart}

	for _, mode := range modes {
		for _, hook := range hooks {
			if mode == sandboxboot.BootModeRepoImage && hook == sandboxboot.HookSetup {
				continue // the one cell workspaceMoved DOES affect -- covered above
			}
			mode, hook := mode, hook
			for _, primary := range []bool{true, false} {
				primary := primary
				name := string(mode) + "/" + string(hook) + "/primary=" + boolString(primary)
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					whenFalse := sandboxboot.EvaluateHook(mode, hook, primary, false)
					whenTrue := sandboxboot.EvaluateHook(mode, hook, primary, true)
					if whenFalse != whenTrue {
						t.Errorf("EvaluateHook(%s, %s, primary=%v) depends on workspaceMoved (false=%+v, true=%+v), want identical",
							mode, hook, primary, whenFalse, whenTrue)
					}
				})
			}
		}
	}
}

// TestEvaluateHook_HookDelta_TruthTable proves §19.6's own new
// policy row (HookDelta, "sync.sh"): eligible under the EXACT SAME envelope
// as HookSetup's own repo_image branch (mode == BootModeRepoImage &&
// workspaceMoved), for both primary values, and FatalOnFailure always
// false -- mirroring TestEvaluateHook_RepoImageSetup_WorkspaceMovedAmendment's
// own structure exactly, one dedicated table per new/changed cell rather
// than folding a third hook value into TestEvaluateHook_TruthTable's own
// pinned 16-row count.
func TestEvaluateHook_HookDelta_TruthTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode           sandboxboot.BootMode
		primary        bool
		workspaceMoved bool
		wantShouldRun  bool
	}{
		{sandboxboot.BootModeBuild, true, true, false},
		{sandboxboot.BootModeBuild, false, true, false},
		{sandboxboot.BootModeFresh, true, true, false},
		{sandboxboot.BootModeFresh, false, true, false},
		{sandboxboot.BootModeSnapshotRestore, true, true, false},
		{sandboxboot.BootModeSnapshotRestore, false, true, false},
		{sandboxboot.BootModeRepoImage, true, false, false},
		{sandboxboot.BootModeRepoImage, false, false, false},
		{sandboxboot.BootModeRepoImage, true, true, true},
		{sandboxboot.BootModeRepoImage, false, true, true},
	}

	for _, tc := range tests {
		name := string(tc.mode) + "/primary=" + boolString(tc.primary) + "/workspaceMoved=" + boolString(tc.workspaceMoved)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := sandboxboot.EvaluateHook(tc.mode, sandboxboot.HookDelta, tc.primary, tc.workspaceMoved)
			want := sandboxboot.HookOutcome{ShouldRun: tc.wantShouldRun, FatalOnFailure: false}
			if got != want {
				t.Errorf("EvaluateHook(%s, sync.sh, primary=%v, workspaceMoved=%v) = %+v, want %+v",
					tc.mode, tc.primary, tc.workspaceMoved, got, want)
			}
		})
	}
}

func TestEvaluateHook_UnrecognizedHook(t *testing.T) {
	t.Parallel()

	got := sandboxboot.EvaluateHook(sandboxboot.BootModeFresh, sandboxboot.Hook("bogus.sh"), true, false)
	want := sandboxboot.HookOutcome{}
	if got != want {
		t.Errorf("EvaluateHook with an unrecognized Hook = %+v, want zero value %+v", got, want)
	}
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
