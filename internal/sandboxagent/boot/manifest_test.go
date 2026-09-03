package boot_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/sandboxagent/boot"
)

func TestLoadImageManifest_RealFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")
	builtAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	content := `{
		"fingerprint": "fp-abc123",
		"built_at": "` + builtAt.Format(time.RFC3339) + `",
		"built_repo_shas": {"repo-a": "sha-a", "repo-b": "sha-b"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, found, err := boot.LoadImageManifest(path)
	if err != nil {
		t.Fatalf("LoadImageManifest() error = %v, want nil", err)
	}
	if !found {
		t.Fatal("LoadImageManifest() found = false, want true for a real, well-formed file")
	}
	if manifest.Fingerprint != "fp-abc123" {
		t.Errorf("Fingerprint = %q, want %q", manifest.Fingerprint, "fp-abc123")
	}
	if !manifest.BuiltAt.Equal(builtAt) {
		t.Errorf("BuiltAt = %v, want %v", manifest.BuiltAt, builtAt)
	}
	if manifest.BuiltRepoShas["repo-a"] != "sha-a" || manifest.BuiltRepoShas["repo-b"] != "sha-b" {
		t.Errorf("BuiltRepoShas = %v, want {repo-a: sha-a, repo-b: sha-b}", manifest.BuiltRepoShas)
	}
}

func TestLoadImageManifest_MissingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	manifest, found, err := boot.LoadImageManifest(path)
	if err != nil {
		t.Errorf("LoadImageManifest() error = %v, want nil for a missing file (expected, non-error case)", err)
	}
	if found {
		t.Error("LoadImageManifest() found = true, want false for a missing file")
	}
	if manifest.Fingerprint != "" || len(manifest.BuiltRepoShas) != 0 {
		t.Errorf("LoadImageManifest() manifest = %+v, want the zero value on a miss", manifest)
	}
}

func TestLoadImageManifest_MalformedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, found, err := boot.LoadImageManifest(path)
	if err == nil {
		t.Fatal("LoadImageManifest() error = nil, want a real error for a malformed file")
	}
	if found {
		t.Error("LoadImageManifest() found = true, want false for a malformed file")
	}
	if manifest.Fingerprint != "" {
		t.Errorf("LoadImageManifest() manifest = %+v, want the zero value on a parse failure", manifest)
	}
}

// The following tests cover B9: built_at must be tolerant of encodings
// other than Go's own RFC 3339 time.Time default, because the build
// service that bakes /narvi/image-manifest.json is an external, non-Go
// component (§19.1) with nothing pinning it to RFC 3339. In every case,
// built_repo_shas -- the load-bearing field in the same document -- must
// still be read, found must still be true, and err must still be nil: an
// unrecognized built_at is a diagnostic-only hiccup, never a reason to
// discard the manifest.

func TestLoadImageManifest_BuiltAtUnixEpochSeconds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")
	// 1785283200 == 2026-07-28T10:40:00Z (Unix epoch seconds -- an entirely
	// ordinary encoding for a non-Go build service to emit).
	content := `{
		"fingerprint": "fp-epoch-secs",
		"built_at": 1785283200,
		"built_repo_shas": {"repo-a": "sha-a"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, found, err := boot.LoadImageManifest(path)
	if err != nil {
		t.Fatalf("LoadImageManifest() error = %v, want nil for a Unix-epoch-seconds built_at", err)
	}
	if !found {
		t.Fatal("LoadImageManifest() found = false, want true for a Unix-epoch-seconds built_at")
	}
	want := time.Unix(1785283200, 0).UTC()
	if !manifest.BuiltAt.Equal(want) {
		t.Errorf("BuiltAt = %v, want %v", manifest.BuiltAt, want)
	}
	if manifest.BuiltRepoShas["repo-a"] != "sha-a" {
		t.Errorf("BuiltRepoShas = %v, want {repo-a: sha-a} -- the load-bearing field must survive an unusual built_at", manifest.BuiltRepoShas)
	}
}

func TestLoadImageManifest_BuiltAtUnixEpochMillis(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")
	content := `{
		"fingerprint": "fp-epoch-millis",
		"built_at": 1785283200000,
		"built_repo_shas": {"repo-a": "sha-a"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, found, err := boot.LoadImageManifest(path)
	if err != nil {
		t.Fatalf("LoadImageManifest() error = %v, want nil for a Unix-epoch-milliseconds built_at", err)
	}
	if !found {
		t.Fatal("LoadImageManifest() found = false, want true for a Unix-epoch-milliseconds built_at")
	}
	want := time.UnixMilli(1785283200000).UTC()
	if !manifest.BuiltAt.Equal(want) {
		t.Errorf("BuiltAt = %v, want %v", manifest.BuiltAt, want)
	}
	if manifest.BuiltRepoShas["repo-a"] != "sha-a" {
		t.Errorf("BuiltRepoShas = %v, want {repo-a: sha-a}", manifest.BuiltRepoShas)
	}
}

func TestLoadImageManifest_BuiltAtSpaceSeparated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")
	content := `{
		"fingerprint": "fp-space-sep",
		"built_at": "2026-07-28 10:00:00Z",
		"built_repo_shas": {"repo-a": "sha-a"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, found, err := boot.LoadImageManifest(path)
	if err != nil {
		t.Fatalf("LoadImageManifest() error = %v, want nil for a space-separated built_at", err)
	}
	if !found {
		t.Fatal("LoadImageManifest() found = false, want true for a space-separated built_at")
	}
	want := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	if !manifest.BuiltAt.Equal(want) {
		t.Errorf("BuiltAt = %v, want %v", manifest.BuiltAt, want)
	}
	if manifest.BuiltRepoShas["repo-a"] != "sha-a" {
		t.Errorf("BuiltRepoShas = %v, want {repo-a: sha-a}", manifest.BuiltRepoShas)
	}
}

// TestLoadImageManifest_BuiltAtUnrecognized_StillReadsRepoShas is B9's
// central case: a built_at shape this reader cannot interpret at all (not
// RFC 3339, not epoch seconds/millis, not any tolerated near-miss) must
// still leave the manifest usable -- found stays true, err stays nil,
// BuiltRepoShas still decodes, and (proven below) ComputeWorkspaceMoved
// still consults it correctly. Only BuiltAt itself is left at its zero
// value, which is the correct outcome for a field nothing downstream
// reads for a decision.
func TestLoadImageManifest_BuiltAtUnrecognized_StillReadsRepoShas(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")
	content := `{
		"fingerprint": "fp-garbage-builtat",
		"built_at": "not-a-timestamp-at-all",
		"built_repo_shas": {"repo-unmoved": "sha-same", "repo-moved": "sha-old"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, found, err := boot.LoadImageManifest(path)
	if err != nil {
		t.Fatalf("LoadImageManifest() error = %v, want nil -- an unrecognized built_at must never fail the whole manifest", err)
	}
	if !found {
		t.Fatal("LoadImageManifest() found = false, want true -- an unrecognized built_at must never be conflated with 'no manifest at all'")
	}
	if !manifest.BuiltAt.IsZero() {
		t.Errorf("BuiltAt = %v, want the zero value for an unrecognized encoding", manifest.BuiltAt)
	}
	if manifest.BuiltRepoShas["repo-unmoved"] != "sha-same" || manifest.BuiltRepoShas["repo-moved"] != "sha-old" {
		t.Fatalf("BuiltRepoShas = %v, want {repo-unmoved: sha-same, repo-moved: sha-old} -- the load-bearing field must survive an unreadable built_at", manifest.BuiltRepoShas)
	}

	// §19.4's workspaceMoved computation must still work correctly from
	// this manifest -- an unreadable built_at must not degrade to the
	// missing-manifest safe default (every repo forced to moved=true);
	// per-repo SHA comparison must still happen.
	current := map[string]string{"repo-unmoved": "sha-same", "repo-moved": "sha-new"}
	moved := boot.ComputeWorkspaceMoved(manifest, found, current)
	if moved["repo-unmoved"] {
		t.Errorf(`ComputeWorkspaceMoved()["repo-unmoved"] = true, want false -- SHA matches, unaffected by built_at`)
	}
	if !moved["repo-moved"] {
		t.Errorf(`ComputeWorkspaceMoved()["repo-moved"] = false, want true -- SHA genuinely differs`)
	}
}

// TestLoadImageManifest_BuiltAtOutOfRangeNumber is a regression test for a
// CONFIRMED B9 review finding: a built_at numeric value large enough to
// overflow float64->int64 conversion (e.g. a build-service bug emitting
// nanoseconds instead of seconds/millis, or a garbage/sentinel value) must
// be rejected as unparseable (ok=false, logged, BuiltAt left at the zero
// value) -- never silently "succeed" with a nonsense time.Time produced by
// an overflowed int64(num) conversion. Before the fix, int64(1e30)
// overflows and clamps to math.MaxInt64 on this Go toolchain, producing
// BuiltAt ~= year 292278994 with found=true and no warning logged --
// exactly the "unparseable value reads as real data downstream" outcome
// this whole field's diagnostic-only contract exists to avoid.
func TestLoadImageManifest_BuiltAtOutOfRangeNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		builtAt string
	}{
		{name: "far_future_overflow", builtAt: "1e30"},
		{name: "far_past_overflow", builtAt: "-1e30"},
		{name: "implausibly_far_future_no_overflow", builtAt: "9999999999999999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "image-manifest.json")
			content := `{
				"fingerprint": "fp-out-of-range-builtat",
				"built_at": ` + tt.builtAt + `,
				"built_repo_shas": {"repo-a": "sha-a"}
			}`
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			manifest, found, err := boot.LoadImageManifest(path)
			if err != nil {
				t.Fatalf("LoadImageManifest() error = %v, want nil -- an out-of-range built_at must never fail the whole manifest", err)
			}
			if !found {
				t.Fatal("LoadImageManifest() found = false, want true -- an out-of-range built_at must never be conflated with 'no manifest at all'")
			}
			if !manifest.BuiltAt.IsZero() {
				t.Errorf("BuiltAt = %v, want the zero value -- an out-of-range numeric built_at must be rejected, not silently clamped into a nonsense time.Time", manifest.BuiltAt)
			}
			if manifest.BuiltRepoShas["repo-a"] != "sha-a" {
				t.Errorf("BuiltRepoShas = %v, want {repo-a: sha-a}", manifest.BuiltRepoShas)
			}
		})
	}
}

// TestLoadImageManifest_BuiltAtEdgeCases pins down B9's remaining edge
// cases that were exercised manually during review but not previously
// asserted by the committed suite: built_at absent, null, an empty
// string, and wrong JSON types (object, array, bool). In every case the
// manifest as a whole must still load successfully (found=true, err=nil,
// built_repo_shas intact) with BuiltAt left at its zero value -- pinning
// this down guards against a future refactor of parseBuiltAt or
// manifestWire silently reintroducing a regression (e.g. a panic on
// raw[0] indexing for an empty RawMessage, or rejecting the whole
// manifest again on an unexpected shape).
func TestLoadImageManifest_BuiltAtEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		manifestJSON   string
		wantErrNil     bool
		wantFound      bool
		wantBuiltAtSet bool
	}{
		{
			name:         "absent_key",
			manifestJSON: `{"fingerprint": "fp-absent", "built_repo_shas": {"repo-a": "sha-a"}}`,
		},
		{
			name:         "null",
			manifestJSON: `{"fingerprint": "fp-null", "built_at": null, "built_repo_shas": {"repo-a": "sha-a"}}`,
		},
		{
			name:         "empty_string",
			manifestJSON: `{"fingerprint": "fp-empty-string", "built_at": "", "built_repo_shas": {"repo-a": "sha-a"}}`,
		},
		{
			name:         "wrong_type_object",
			manifestJSON: `{"fingerprint": "fp-wrong-type-object", "built_at": {}, "built_repo_shas": {"repo-a": "sha-a"}}`,
		},
		{
			name:         "wrong_type_array",
			manifestJSON: `{"fingerprint": "fp-wrong-type-array", "built_at": [], "built_repo_shas": {"repo-a": "sha-a"}}`,
		},
		{
			name:         "wrong_type_bool",
			manifestJSON: `{"fingerprint": "fp-wrong-type-bool", "built_at": true, "built_repo_shas": {"repo-a": "sha-a"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "image-manifest.json")
			if err := os.WriteFile(path, []byte(tt.manifestJSON), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			manifest, found, err := boot.LoadImageManifest(path)
			if err != nil {
				t.Fatalf("LoadImageManifest() error = %v, want nil for built_at case %q", err, tt.name)
			}
			if !found {
				t.Fatalf("LoadImageManifest() found = false, want true for built_at case %q", tt.name)
			}
			if !manifest.BuiltAt.IsZero() {
				t.Errorf("BuiltAt = %v, want the zero value for built_at case %q", manifest.BuiltAt, tt.name)
			}
			if manifest.BuiltRepoShas["repo-a"] != "sha-a" {
				t.Errorf("BuiltRepoShas = %v, want {repo-a: sha-a} for built_at case %q -- the load-bearing field must survive", manifest.BuiltRepoShas, tt.name)
			}
		})
	}
}

// TestLoadImageManifest_WholeManifestWrongShape pins down the pre-existing
// (not a B9 regression) behavior for a syntactically valid JSON document
// that is the wrong overall shape -- a bare `null` or `{}` -- confirmed
// during B9 review to be harmless: LoadImageManifest reports found=true
// with an empty/nil BuiltRepoShas map, and ComputeWorkspaceMoved's own
// !ok safe-default still marks every repo moved=true for such a manifest
// (identical to the missing-manifest case), so this never produces the
// feared workspaceMoved=false-everywhere outcome. Pinned here so a future
// change to this contract is a deliberate, visible decision rather than a
// silent behavior change.
func TestLoadImageManifest_WholeManifestWrongShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		manifestJSON string
	}{
		{name: "bare_null", manifestJSON: `null`},
		{name: "empty_object", manifestJSON: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "image-manifest.json")
			if err := os.WriteFile(path, []byte(tt.manifestJSON), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			manifest, found, err := boot.LoadImageManifest(path)
			if err != nil {
				t.Fatalf("LoadImageManifest() error = %v, want nil for a wrong-shape-but-valid-JSON manifest %q", err, tt.name)
			}
			if !found {
				t.Fatalf("LoadImageManifest() found = false, want true for a wrong-shape-but-valid-JSON manifest %q", tt.name)
			}
			if len(manifest.BuiltRepoShas) != 0 {
				t.Errorf("BuiltRepoShas = %v, want empty for a wrong-shape manifest %q", manifest.BuiltRepoShas, tt.name)
			}

			// The safe default absorbs this: every repo still reports
			// moved=true, identical to the missing-manifest case.
			current := map[string]string{"repo-a": "sha-a"}
			moved := boot.ComputeWorkspaceMoved(manifest, found, current)
			if !moved["repo-a"] {
				t.Errorf("ComputeWorkspaceMoved()[%q] = false, want true -- a wrong-shape manifest's empty BuiltRepoShas must still safe-default every repo to moved=true", "repo-a")
			}
		})
	}
}

func TestComputeWorkspaceMoved_ManifestFound_ComparesPerRepoSHA(t *testing.T) {
	t.Parallel()

	manifest := boot.ImageManifest{
		BuiltRepoShas: map[string]string{
			"repo-unmoved": "sha-same",
			"repo-moved":   "sha-old",
			// "repo-no-entry" deliberately absent.
		},
	}
	current := map[string]string{
		"repo-unmoved":  "sha-same",
		"repo-moved":    "sha-new",
		"repo-no-entry": "sha-whatever",
	}

	got := boot.ComputeWorkspaceMoved(manifest, true, current)

	want := map[string]bool{
		"repo-unmoved":  false,
		"repo-moved":    true,
		"repo-no-entry": true, // no manifest entry for this repo -- safe default: treat as moved
	}
	for name, wantMoved := range want {
		if got[name] != wantMoved {
			t.Errorf("ComputeWorkspaceMoved()[%q] = %v, want %v", name, got[name], wantMoved)
		}
	}
}

// TestComputeWorkspaceMoved_ManifestNotFound_EveryRepoDefaultsToMoved proves
// this Step's own resolved safe-default decision (§19.4): a missing or
// unreadable manifest makes EVERY repo report workspaceMoved: true,
// unconditionally -- never silently defaulting to false, which would
// reopen exactly the "missing dependency" gap this design exists to close.
func TestComputeWorkspaceMoved_ManifestNotFound_EveryRepoDefaultsToMoved(t *testing.T) {
	t.Parallel()

	current := map[string]string{"repo-a": "sha-a", "repo-b": "sha-b"}

	got := boot.ComputeWorkspaceMoved(boot.ImageManifest{}, false, current)

	for name := range current {
		if !got[name] {
			t.Errorf("ComputeWorkspaceMoved()[%q] = false, want true (manifest not found -- safe default)", name)
		}
	}
}
