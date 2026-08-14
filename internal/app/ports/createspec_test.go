package ports

import (
	"errors"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
)

func TestCreateSpec_Validate(t *testing.T) {
	t.Run("Gen matches SessionConfig.Gen", func(t *testing.T) {
		spec := CreateSpec{Gen: 3, SessionConfig: sessionconfig.SessionConfig{Gen: 3}}
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("Gen diverges from SessionConfig.Gen", func(t *testing.T) {
		spec := CreateSpec{Gen: 3, SessionConfig: sessionconfig.SessionConfig{Gen: 1}}
		err := spec.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want a *GenMismatchError")
		}
		var target *GenMismatchError
		if !errors.As(err, &target) {
			t.Fatalf("Validate() error = %v, want *GenMismatchError", err)
		}
		if target.Gen != 3 || target.SessionConfigGen != 1 {
			t.Errorf("GenMismatchError = %+v, want Gen=3 SessionConfigGen=1", target)
		}
		for _, want := range []string{"3", "1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
			}
		}
	})
}

// TestImageSpec_CacheMount_NilByDefault proves a zero-value/pre-existing
// ImageSpec literal (every fixture written before this field existed)
// still has CacheMount == nil — "no cache requested" must be the ordinary
// zero value, not something a caller has to opt out of explicitly.
func TestImageSpec_CacheMount_NilByDefault(t *testing.T) {
	spec := ImageSpec{Base: "base:v1", RuntimeVersion: "1.0.0"}
	if spec.CacheMount != nil {
		t.Errorf("ImageSpec{}.CacheMount = %+v, want nil", spec.CacheMount)
	}
}

// TestImageSpec_CacheMount_CarriesKeyAndPaths is a plain data-shape smoke
// test: CacheMount round-trips exactly the Key/Paths it was constructed
// with, with no hidden normalization/mutation.
func TestImageSpec_CacheMount_CarriesKeyAndPaths(t *testing.T) {
	mount := &CacheMount{
		Key:   "deadbeef",
		Paths: []string{"/root/.npm/_cacache", "/root/.cache/pip"},
	}
	spec := ImageSpec{Base: "base:v1", RuntimeVersion: "1.0.0", CacheMount: mount}

	if spec.CacheMount.Key != "deadbeef" {
		t.Errorf("CacheMount.Key = %q, want %q", spec.CacheMount.Key, "deadbeef")
	}
	if len(spec.CacheMount.Paths) != 2 {
		t.Errorf("CacheMount.Paths = %v, want 2 entries", spec.CacheMount.Paths)
	}
}
