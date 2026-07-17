package sandboxboot_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
)

// TestEvaluateHook_TruthTable enumerates every row of the Step's own
// 16-row truth table (§6.4's hook policy applied to all 4 modes x 2 hooks
// x 2 primary values), one sub-test per row, so a single wrong cell fails
// with a readable name naming exactly which (mode, hook, primary) combo
// broke.
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

			got := sandboxboot.EvaluateHook(tc.mode, tc.hook, tc.primary)
			if got.ShouldRun != tc.wantShouldRun {
				t.Errorf("EvaluateHook(%s, %s, primary=%v).ShouldRun = %v, want %v",
					tc.mode, tc.hook, tc.primary, got.ShouldRun, tc.wantShouldRun)
			}
			if got.FatalOnFailure != tc.wantFatal {
				t.Errorf("EvaluateHook(%s, %s, primary=%v).FatalOnFailure = %v, want %v",
					tc.mode, tc.hook, tc.primary, got.FatalOnFailure, tc.wantFatal)
			}
		})
	}
}

func TestEvaluateHook_UnrecognizedHook(t *testing.T) {
	t.Parallel()

	got := sandboxboot.EvaluateHook(sandboxboot.BootModeFresh, sandboxboot.Hook("bogus.sh"), true)
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
