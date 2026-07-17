package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/khazaddev/narvi/internal/domain/servicemanifest"
)

// manifestRelPath is where a services.yml lives within a repo, relative to
// the repo's own root (§14.2: "New optional .narvi/services.yml per
// repo").
const manifestRelPath = ".narvi/services.yml"

// Locate reports the path to <repoDir>/.narvi/services.yml and whether it
// exists. A genuine stat error other than "does not exist" (e.g.
// permission denied) is a real error, propagated as-is -- mirroring
// internal/sandboxagent/boot/hooks.go's own hookScriptPresent, which draws
// exactly this same distinction for setup.sh/start.sh.
func Locate(repoDir string) (path string, found bool, err error) {
	path = filepath.Join(repoDir, filepath.FromSlash(manifestRelPath))

	_, statErr := os.Stat(path)
	if statErr == nil {
		return path, true, nil
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return path, false, nil
	}
	return path, false, statErr
}

// Load reads the manifest at path, unmarshals its YAML into a
// servicemanifest.RawManifest (this package owns that impure step -- the
// domain package itself never touches YAML, see
// internal/domain/servicemanifest's own doc.go), and validates the result
// via servicemanifest.Validate.
func Load(path string) (servicemanifest.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return servicemanifest.Manifest{}, fmt.Errorf("services: read %s: %w", path, err)
	}

	var raw servicemanifest.RawManifest
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return servicemanifest.Manifest{}, fmt.Errorf("services: parse yaml %s: %w", path, err)
	}

	manifest, err := servicemanifest.Validate(raw)
	if err != nil {
		return servicemanifest.Manifest{}, fmt.Errorf("services: validate %s: %w", path, err)
	}
	return manifest, nil
}
