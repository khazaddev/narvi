package seed

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/narvidev/narvi/internal/domain/seedmanifest"
)

// LoadManifest reads path (the ONLY I/O in this package that touches the
// filesystem -- everything else is Postgres via the stores threaded
// through Deps), decodes it as YAML into a seedmanifest.Manifest, and
// structurally validates it via seedmanifest.Validate before returning.
// A YAML document containing a key this schema does not recognize (e.g.
// a "role" field an operator mistakenly added to a participant entry --
// see seedmanifest's own doc comment on why no such field exists) is
// REJECTED outright: yaml.Decoder.KnownFields(true) below turns an
// unknown key into a decode error rather than silently discarding it,
// so a manifest author who tries to smuggle in a field this schema does
// not define learns about it immediately, not by discovering later that
// it was silently ignored.
func LoadManifest(path string) (*seedmanifest.Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("seed: read manifest %s: %w", path, err)
	}

	var m seedmanifest.Manifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("seed: parse manifest %s: %w", path, err)
	}

	if err := seedmanifest.Validate(m); err != nil {
		return nil, fmt.Errorf("seed: manifest %s failed validation: %w", path, err)
	}

	return &m, nil
}
