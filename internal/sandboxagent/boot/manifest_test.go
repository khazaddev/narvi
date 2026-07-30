package boot_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
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
