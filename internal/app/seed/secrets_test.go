package seed

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/seedmanifest"
)

func TestSecretScopeTarget(t *testing.T) {
	t.Parallel()

	t.Run("global has nil scopeTargetID", func(t *testing.T) {
		t.Parallel()
		scope, target := secretScopeTarget(seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "X"})
		if scope != sqlcgen.SandboxSecretScopeGlobal {
			t.Errorf("scope = %v, want global", scope)
		}
		if target != nil {
			t.Errorf("scopeTargetID = %v, want nil", *target)
		}
	})

	t.Run("repo carries repoFullName as scopeTargetID", func(t *testing.T) {
		t.Parallel()
		scope, target := secretScopeTarget(seedmanifest.Secret{Scope: seedmanifest.SecretScopeRepo, RepoFullName: "example-org/widget-app", Name: "X"})
		if scope != sqlcgen.SandboxSecretScopeRepo {
			t.Errorf("scope = %v, want repo", scope)
		}
		if target == nil || *target != "example-org/widget-app" {
			t.Errorf("scopeTargetID = %v, want example-org/widget-app", target)
		}
	})
}

// TestSecretKey_NeverContainsValue is a mutation guard for report.go's
// own "an Item never carries a secret value" invariant, specifically at
// this package's one entry point for it: secretKey is what becomes
// Item.Key for every secret this package processes (see seedSecret) --
// if it were ever changed to interpolate s.Value (e.g. "for debugging"),
// this test catches it immediately.
func TestSecretKey_NeverContainsValue(t *testing.T) {
	t.Parallel()
	const distinctiveSecretValue = "sk-live-do-not-print-this-anywhere-1234567890"
	s := seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_TOKEN", Value: distinctiveSecretValue}
	if got := secretKey(s); strings.Contains(got, distinctiveSecretValue) {
		t.Fatalf("secretKey(%+v) = %q, must never contain the secret value", s, got)
	}
}
