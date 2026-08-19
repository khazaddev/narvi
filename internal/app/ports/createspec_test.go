package ports

import (
	"errors"
	"reflect"
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

	t.Run("Docker matches SessionConfig.Docker", func(t *testing.T) {
		spec := CreateSpec{SessionConfig: sessionconfig.SessionConfig{Docker: true}, Docker: true}
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("Docker diverges from SessionConfig.Docker", func(t *testing.T) {
		spec := CreateSpec{SessionConfig: sessionconfig.SessionConfig{Docker: false}, Docker: true}
		err := spec.Validate()
		var target *DockerMismatchError
		if !errors.As(err, &target) {
			t.Fatalf("Validate() error = %v, want *DockerMismatchError", err)
		}
		if target.Docker != true || target.SessionConfigDocker != false {
			t.Errorf("DockerMismatchError = %+v, want Docker=true SessionConfigDocker=false", target)
		}
	})

	t.Run("EgressPolicy nil on both sides matches", func(t *testing.T) {
		spec := CreateSpec{SessionConfig: sessionconfig.SessionConfig{}}
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("EgressPolicy matches SessionConfig.EgressPolicy", func(t *testing.T) {
		policy := &sessionconfig.SessionConfigEgressPolicy{
			Mode:      sessionconfig.SessionConfigEgressPolicyModeAllowlist,
			Allowlist: []string{"github.com", "cp.example.com"},
		}
		spec := CreateSpec{
			SessionConfig: sessionconfig.SessionConfig{EgressPolicy: policy},
			EgressPolicy:  policy,
		}
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("EgressPolicy diverges: nil vs non-nil", func(t *testing.T) {
		policy := &sessionconfig.SessionConfigEgressPolicy{Mode: sessionconfig.SessionConfigEgressPolicyModeOpen, Allowlist: nil}
		spec := CreateSpec{
			SessionConfig: sessionconfig.SessionConfig{EgressPolicy: policy},
			EgressPolicy:  nil,
		}
		err := spec.Validate()
		var target *EgressPolicyMismatchError
		if !errors.As(err, &target) {
			t.Fatalf("Validate() error = %v, want *EgressPolicyMismatchError", err)
		}
	})

	t.Run("EgressPolicy diverges: different allowlist contents", func(t *testing.T) {
		specPolicy := &sessionconfig.SessionConfigEgressPolicy{Mode: sessionconfig.SessionConfigEgressPolicyModeAllowlist, Allowlist: []string{"a.example.com"}}
		configPolicy := &sessionconfig.SessionConfigEgressPolicy{Mode: sessionconfig.SessionConfigEgressPolicyModeAllowlist, Allowlist: []string{"b.example.com"}}
		spec := CreateSpec{
			SessionConfig: sessionconfig.SessionConfig{EgressPolicy: configPolicy},
			EgressPolicy:  specPolicy,
		}
		err := spec.Validate()
		var target *EgressPolicyMismatchError
		if !errors.As(err, &target) {
			t.Fatalf("Validate() error = %v, want *EgressPolicyMismatchError", err)
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

// TestImageSpec_HasNoSecretCarryingField makes §19.8 rule (a)'s "never
// passed to BuildImage" property STRUCTURAL, not merely observed: it
// pins ImageSpec's own exact field set via reflection, so a future PR
// that adds ANY new field to this type -- in particular, a per-scope
// user-configurable env map (sandbox_secrets, Step 72, §27.1) or an
// opaque "extra env" escape hatch -- fails this test immediately and
// loudly, rather than silently acquiring a channel §19.8 exists to
// forbid. This is deliberately an EXHAUSTIVE field-name check (not just
// "no field literally named Env/Secrets exists") specifically so a
// differently-named field with the same effect (e.g. "ExtraVars",
// "BuildTimeConfig") is caught too -- the test fails on ANY field this
// list doesn't already name, forcing a conscious, reviewed decision
// (updating this list, ideally alongside a §19.8 rule-(b) fingerprint
// change, per that section's own documented escape hatch) rather than a
// silent drift.
//
// cmd/sandbox-agent/main.go's own sandboxsecrets.go/opencodeconfig.go
// (Step 72) never construct or touch a ports.ImageSpec/CreateSpec at
// all -- grepped for before writing this test -- so the OTHER half of
// the "never reaches BuildImage" property (that sandbox-agent's own
// fetch code is never even CALLED during a real image build, because a
// build-mode boot's own boot.Config.SessionConfig is nil) is proven
// separately, by cmd/sandbox-agent's own
// TestFetchSandboxSecrets_NilSessionConfigPanics/
// TestFetchOpenCodeConfig_NilSessionConfigPanics (sandboxsecrets_test.go/
// opencodeconfig_test.go): a nil SessionConfig -- exactly what a real
// image build's own boot.Config carries -- makes either fetch function
// panic immediately on its first field access, rather than silently
// proceeding with a malformed request. This test covers the type-shape
// half: even if some FUTURE caller tried to smuggle a secret through
// anyway, ImageSpec's own shape gives it nowhere to put one.
func TestImageSpec_HasNoSecretCarryingField(t *testing.T) {
	want := map[string]bool{
		"Base":           true,
		"Repos":          true,
		"RuntimeVersion": true,
		"CacheMount":     true,
	}

	typ := reflect.TypeOf(ImageSpec{})
	if typ.NumField() != len(want) {
		names := make([]string, 0, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			names = append(names, typ.Field(i).Name)
		}
		t.Fatalf("ImageSpec has %d fields %v, want exactly %d: %v -- if this is a deliberate, reviewed addition (§19.8 rule (a)/(b)), update this test's own `want` map; otherwise a new field just acquired a channel §19.8 forbids", typ.NumField(), names, len(want), want)
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !want[name] {
			t.Errorf("ImageSpec has unexpected field %q -- see this test's own doc comment", name)
		}
	}
}
