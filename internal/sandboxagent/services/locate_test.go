package services_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/narvidev/narvi/internal/sandboxagent/services"
)

func TestLocate_Found(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".narvi"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifestPath := filepath.Join(repoDir, ".narvi", "services.yml")
	if err := os.WriteFile(manifestPath, []byte("services:\n  - name: web\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	path, found, err := services.Locate(repoDir)
	if err != nil {
		t.Fatalf("Locate() error = %v, want nil", err)
	}
	if !found {
		t.Fatal("Locate() found = false, want true")
	}
	if path != manifestPath {
		t.Errorf("Locate() path = %q, want %q", path, manifestPath)
	}
}

func TestLocate_Absent(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()

	_, found, err := services.Locate(repoDir)
	if err != nil {
		t.Fatalf("Locate() error = %v, want nil (absent manifest is routine, not an error)", err)
	}
	if found {
		t.Fatal("Locate() found = true, want false")
	}
}

// TestLocate_GenuineStatError proves a real stat failure other than "does
// not exist" (here: ENOTDIR, because a path component that should be a
// directory is actually a regular file) is propagated as a real error, not
// silently folded into "absent" -- mirroring
// internal/sandboxagent/boot/hooks.go's own hookScriptPresent distinction.
func TestLocate_GenuineStatError(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	// .narvi is a regular FILE here, not a directory -- so stat'ing
	// <repoDir>/.narvi/services.yml fails with ENOTDIR, not ENOENT.
	if err := os.WriteFile(filepath.Join(repoDir, ".narvi"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, found, err := services.Locate(repoDir)
	if err == nil {
		t.Fatal("Locate() error = nil, want a genuine stat error (ENOTDIR)")
	}
	if found {
		t.Error("Locate() found = true, want false alongside a genuine error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("Locate() error = %v, want something OTHER than ErrNotExist (this is ENOTDIR, a real failure)", err)
	}
}

func TestLoad_ValidManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "services.yml")
	content := "services:\n  - name: web\n    cmd: pnpm dev\n    readiness: { port: 3000 }\n    criticality: primary\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, err := services.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if len(manifest.Services) != 1 || manifest.Services[0].Name != "web" {
		t.Errorf("Load() manifest = %+v, want one service named web", manifest)
	}
}

func TestLoad_InvalidManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "services.yml")
	// Present but empty services list -- a validation error per
	// servicemanifest.Validate (EmptyServicesError), which Load must
	// propagate rather than mask.
	if err := os.WriteFile(path, []byte("services: []\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := services.Load(path); err == nil {
		t.Fatal("Load() error = nil, want the underlying servicemanifest.Validate validation error")
	}
}

func TestLoad_FileMissing(t *testing.T) {
	t.Parallel()

	_, err := services.Load(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	if err == nil {
		t.Fatal("Load() error = nil, want a read error for a missing file")
	}
}

// TestLoad_MalformedYAML proves Load surfaces its own yaml.Unmarshal error
// (wrapped) for a document that isn't valid YAML at all -- this package,
// not internal/domain/servicemanifest, owns the YAML-decode step (see
// servicemanifest's own doc.go), so this case is exercised here rather
// than in manifest_test.go.
func TestLoad_MalformedYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "services.yml")
	content := "services: [\n  - this is: not: valid: yaml::: ]["
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := services.Load(path); err == nil {
		t.Fatal("Load() error = nil, want a yaml.Unmarshal error for malformed YAML")
	}
}

// TestLoad_WrongShapeYAML proves Load surfaces its own yaml.Unmarshal error
// for valid YAML that isn't the expected shape (an int where a services
// list is expected) -- same rationale as TestLoad_MalformedYAML above.
func TestLoad_WrongShapeYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "services.yml")
	if err := os.WriteFile(path, []byte("services: 42\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := services.Load(path); err == nil {
		t.Fatal("Load() error = nil, want a yaml.Unmarshal error for the wrong-shape document")
	}
}
