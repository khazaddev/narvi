package seed_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/app/seed"
)

func TestLoadManifest_Valid(t *testing.T) {
	t.Parallel()
	m, err := seed.LoadManifest("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("LoadManifest() = %v, want nil", err)
	}
	if len(m.Participants) != 2 {
		t.Errorf("Participants = %d, want 2", len(m.Participants))
	}
	if len(m.Secrets) != 2 {
		t.Errorf("Secrets = %d, want 2", len(m.Secrets))
	}
	if len(m.Automations) != 1 {
		t.Errorf("Automations = %d, want 1", len(m.Automations))
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	t.Parallel()
	if _, err := seed.LoadManifest("testdata/does-not-exist.yaml"); err == nil {
		t.Fatal("LoadManifest() = nil error, want an error for a missing file")
	}
}

func TestLoadManifest_FailsStructuralValidation(t *testing.T) {
	t.Parallel()
	if _, err := seed.LoadManifest("testdata/invalid.yaml"); err == nil {
		t.Fatal("LoadManifest() = nil error, want a validation error")
	}
}

// TestLoadManifest_RejectsUnknownFieldOnParticipant is a structural
// mutation guard for §13.4's "no path where the seed data itself can
// assert a role" requirement (see internal/domain/seedmanifest's own doc
// comment on why Participant has no Role field): a manifest author who
// adds a "role" key under a participant entry -- exactly the shape an
// attacker or a confused operator might try -- must get a loud parse
// error, never a silent no-op. This is what LoadManifest's own
// dec.KnownFields(true) call exists for; if that call were ever removed
// (or a Role field were ever added to seedmanifest.Participant, letting
// the key parse successfully), this test fails.
func TestLoadManifest_RejectsUnknownFieldOnParticipant(t *testing.T) {
	t.Parallel()
	_, err := seed.LoadManifest("testdata/unknown_field_role.yaml")
	if err == nil {
		t.Fatal("LoadManifest() = nil error, want a decode error for the unrecognized \"role\" field")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Errorf("LoadManifest() error = %q, want it to name the offending \"role\" field", err.Error())
	}
}
